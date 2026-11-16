package logging

import (
	"bytes"
	"net/http"
	"time"
)

// Phase 标明执行阶段。它用于解释失败位置，不参与 HTTP 执行或重试决策。
type Phase string

const (
	// PhaseParameters 表示参数与入口检查阶段，例如参数无效、Context 为 nil 或上游已取消。
	// ApiTo 缺少 Writer 或开启了不支持的正文校验时，也归入此阶段。
	PhaseParameters Phase = "parameters"

	// PhaseRequest 表示构造请求模板阶段，包括解析 URL、合并查询参数、设置 Header 和包装 Body。
	PhaseRequest Phase = "request"

	// PhasePolicy 表示首次发送前执行 URLPolicy 地址检查的阶段。
	// 重定向期间的地址策略拒绝发生在 Client.Do 内，归入 PhaseSend。
	PhasePolicy Phase = "policy"

	// PhaseAdmission 表示显式启用 Limiter 后，计算容量预算及申请共享名额的阶段。
	// 单次预算超过容量、排队期间取消或超时等错误均归入此阶段。
	PhaseAdmission Phase = "admission"

	// PhaseBodyPreparation 表示首次尝试前建立正文重放方案的阶段。
	// 需要重试的一次性流可能在此进行有界缓存；失败时尚未计入任何尝试。
	PhaseBodyPreparation Phase = "body_preparation"

	// PhaseBodyOpen 表示为当前尝试创建请求并打开正文的阶段，包括 BodyFactory 调用和空正文探测。
	// 此时已经计入本次尝试，即使尚未发送 HTTP 请求，失败也会产生 OnAttemptEnd。
	PhaseBodyOpen Phase = "body_open"

	// PhaseSend 表示 Client.Do 执行阶段，覆盖连接建立、请求发送、接收响应头及整条重定向链。
	// 网络错误、重定向被拒绝等导致 Client.Do 返回的错误均归入此阶段。
	PhaseSend Phase = "send"

	// PhaseStatus 表示响应状态码未被成功策略接受，默认是非 2xx 状态。
	// 若同时发生响应正文读取错误，则优先归入 PhaseResponseRead。
	PhaseStatus Phase = "status"

	// PhaseResponseRead 表示读取响应正文失败，包括读取中断、容量超限及读取期间取消或超时。
	// 缓冲返回和 ApiTo 流式输出的响应读取错误均归入此阶段。
	PhaseResponseRead Phase = "response_read"

	// PhaseResponseWrite 表示 ApiTo 向调用方 Writer 写入响应正文失败，包括短写和 Writer 返回错误。
	PhaseResponseWrite Phase = "response_write"

	// PhaseValidation 表示响应正文读取完成后的业务校验失败，包括校验不通过、返回错误或 panic。
	PhaseValidation Phase = "validation"

	// PhaseAttempt 表示尝试结束时复核发现取消或超时的兜底阶段。
	// 当发送、读取和校验原本成功，但清理或同步日志回调后发现期限已到时，使用此值。
	PhaseAttempt Phase = "attempt"

	// PhaseRetryWait 表示已经决定重试、等待退避间隔的阶段。
	// 等待期间取消或超时会终止调用，不会增加下一次尝试及实际重试的计数。
	PhaseRetryWait Phase = "retry_wait"

	// PhaseComplete 表示本次尝试或整次调用成功完成，对应结果的 Err 为 nil。
	PhaseComplete Phase = "complete"
)

// CallInfo 描述一次逻辑调用。RequestID 在本次调用的所有尝试中保持不变。
// TraceID、Caller 来自业务参数；日志实现也可以从 Start 的 Context 中读取业务元数据。
// URL 在 Start 派发前经过配置的 Redactor 处理，默认仅保留 origin。
type CallInfo struct {
	RequestID string
	TraceID   string
	Caller    string
	Method    string
	URL       string
	StartedAt time.Time
}

// BodySnapshot 是正常读流过程中采集的正文副本，不是可再次读取的流。
// Complete 表示执行层确认正文已读完；Truncated 表示已观察到超出采集上限的数据。
// 两者独立：网络中断时可能既不完整，也没有达到采集上限。
// OmittedReason 非空表示 Data 被关闭、未发送或脱敏策略整体省略，不应当作空正文。
// 默认脱敏器不会输出不完整或被截断的 JSON 前缀，以免绕过字段脱敏。
type BodySnapshot struct {
	Data          []byte
	Complete      bool
	Truncated     bool
	OmittedReason string
}

// RequestSnapshot 描述重试循环中一轮开始时的请求，不是抓包结果。
// Header 在 Client.Do 之前采集，不包含 CookieJar、重定向及 Transport 随后补充的头。
// 重定向的各跳不单独产生 AttemptResult，Transport 自身的透明连接重试也不单独计数。
// ContentType 单独保留，使关闭 Header 采集时仍能正确进行正文脱敏。
type RequestSnapshot struct {
	Method        string
	URL           string
	Host          string
	Header        http.Header
	ContentType   string
	ContentLength int64
	Body          BodySnapshot
}

// ResponseSnapshot 描述一次尝试最终获得的响应。重定向时 URL 是最终响应的地址。
// 正文通常是 Go HTTP 层解压后的业务数据；流式输出同样只保留有界副本。
type ResponseSnapshot struct {
	URL           string
	StatusCode    int
	Header        http.Header
	ContentType   string
	ContentLength int64
	Body          BodySnapshot
}

// AttemptResult 在一次尝试完成、上传流清理和超时判定之后派发。
// Attempt 从 1 开始，包含为本次尝试打开 Body 的阶段；该阶段失败也有结束事件。
// Response 为 nil 表示没有收到响应；Err 保留错误链，调用方不得修改其中的数据。
type AttemptResult struct {
	Attempt   int
	StartedAt time.Time
	Duration  time.Duration
	Phase     Phase
	Request   RequestSnapshot
	Response  *ResponseSnapshot
	Err       error
}

// RetryEvent 表示执行层决定尝试重试，Delay 是计划等待时间。
// 此事件在等待之前派发；等待期间可能取消，因此不能据此累加实际重试次数。
// StatusCode 仅表示本轮最终响应；本轮没有响应时为 0，不继承先前尝试的状态。
type RetryEvent struct {
	Attempt     int
	NextAttempt int
	Delay       time.Duration
	StatusCode  int
	Phase       Phase
	Err         error
}

// CallResult 是最终汇总，不保留历次正文以免重试次数放大日志缓存。
// AttemptCount 为实际开始的尝试数；RetryCount=max(AttemptCount-1, 0)。
// 参数、排队或公共正文准备失败时，AttemptCount 可以为 0。
// StatusCode 是本次调用中，执行层最近一次取得的最终响应状态；未取得过响应时为 0。
// 按各轮 Client.Do 返回的 Response 统计，不单独记录内部跳转的中间响应。
// 后一轮没有响应时保留此前状态；成功与否看 Err，单轮是否收到响应看 AttemptResult.Response。
// Duration 包含准备、排队、尝试、重试等待及资源清理，不包含本次 OnEnd 输出耗时。
// 同步的 Start/尝试/重试回调也占用实际调用时间，可能影响总期限。
type CallResult struct {
	AttemptCount int
	RetryCount   int
	Duration     time.Duration

	// LimiterWaitDuration 表示申请 Limiter 名额的耗时，包含 Acquire 的检查开销。
	// 成功和失败都会统计；未启用 Limiter，或在申请前失败时为 0。
	// 它是 Duration 的一部分，不包含名额申请前的预算计算及后续各轮执行。
	LimiterWaitDuration time.Duration

	StatusCode int
	Phase      Phase
	Err        error
}

// Clone 复制可变字段，适合需要在多个日志消费者之间隔离快照的场景。
func (r RequestSnapshot) Clone() RequestSnapshot {
	r.Header = r.Header.Clone()
	r.Body.Data = bytes.Clone(r.Body.Data)
	return r
}

// Clone 复制可变字段；复制结果不共享 Header 或 Body.Data。
func (r ResponseSnapshot) Clone() ResponseSnapshot {
	r.Header = r.Header.Clone()
	r.Body.Data = bytes.Clone(r.Body.Data)
	return r
}
