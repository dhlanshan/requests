package logging

import (
	"context"
	"fmt"
	"log/slog"
)

// Logger 是日志实现的唯一入口，应能被并发调用。
// Start 为一次逻辑调用创建独立 Session；返回 nil 表示本次不记录，执行层也不采集正文。
// Start 不能读取请求正文。它收到的 CallInfo 已经过 URL 脱敏。
type Logger interface {
	Start(context.Context, CallInfo) Session
}

// Session 接收同一次调用的有序事件。实现可立即打印，也可收集摘要后在 OnEnd 汇总。
// 回调同步执行，返回值不参与请求结果；实现应及时返回。框架隔离回调 panic，但无法
// 强制停止不返回的回调。Session 只属于一次调用，不要求 Clone 或 SetTransport。
type Session interface {
	OnAttemptEnd(context.Context, AttemptResult)
	OnRetry(context.Context, RetryEvent)
	OnEnd(context.Context, CallResult)
}

// LoggerFunc 允许使用普通函数创建每次调用的日志会话。
type LoggerFunc func(context.Context, CallInfo) Session

func (f LoggerFunc) Start(ctx context.Context, info CallInfo) Session { return f(ctx, info) }

// SessionFuncs 是函数适配器。未设置的回调是空操作，适合仅关心结束日志的使用者。
type SessionFuncs struct {
	AttemptEnd func(context.Context, AttemptResult)
	Retry      func(context.Context, RetryEvent)
	End        func(context.Context, CallResult)
}

func (f SessionFuncs) OnAttemptEnd(ctx context.Context, r AttemptResult) {
	if f.AttemptEnd != nil {
		f.AttemptEnd(ctx, r)
	}
}
func (f SessionFuncs) OnRetry(ctx context.Context, r RetryEvent) {
	if f.Retry != nil {
		f.Retry(ctx, r)
	}
}
func (f SessionFuncs) OnEnd(ctx context.Context, r CallResult) {
	if f.End != nil {
		f.End(ctx, r)
	}
}

// CallbackError 是被隔离的扩展 panic。只保存发生位置和 panic 类型，不保存原始值，
// 避免默认错误报告意外输出令牌、URL 或正文。它不会作为 HTTP 请求错误返回。
type CallbackError struct{ Operation, PanicType string }

func (e *CallbackError) Error() string {
	return fmt.Sprintf("logging %s panicked (%s)", e.Operation, e.PanicType)
}

// Call 管理一次日志会话的配置、快照隔离和异常边界。它不发送 HTTP 请求。
// 方法对 nil *Call 安全，便于日志关闭时直接跳过；同一个 Call 由调用方串行驱动。
type Call struct {
	config  Config
	session Session
	ended   bool
}

// Start 根据配置开启一次日志会话。本函数使 logging 包也能脱离 requests 独立使用。
// nil 配置、无效配置、被禁用的 Logger 或 Start panic 均返回 nil；配置错误/扩展 panic
// 通过 OnError 报告。requests 同时在参数检查阶段调用 Config.Validate 拒绝无效配置。
func Start(ctx context.Context, config *Config, info CallInfo) *Call {
	if config == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c := &Call{config: *config}
	if err := config.Validate(); err != nil {
		c.report(ctx, err)
		return nil
	}
	c.config.Capture = c.config.Capture.defaults()
	if c.config.Redactor == nil {
		c.config.Redactor = DefaultRedactor{}
	}
	if c.config.Logger == nil {
		c.config.Logger = NewSlogLogger(nil)
	}
	request := c.redactRequest(ctx, RequestSnapshot{Method: info.Method, URL: info.URL})
	info.URL = request.URL
	c.safe(ctx, "start", func() { c.session = c.config.Logger.Start(ctx, info) })
	if nilValue(c.session) {
		return nil
	}
	return c
}

// Capture 返回本次会话实际使用的采集配置；nil Call 不采集任何正文或 Header。
func (c *Call) Capture() CaptureOptions {
	if c == nil {
		return CaptureOptions{}
	}
	return c.config.Capture
}

// OnAttemptEnd 复制并脱敏请求/响应，再派发给用户。之后执行层可释放或复用原数据。
// 原始 Err 仅按只读错误链传递，不尝试复制任意用户定义的 error 实现。
func (c *Call) OnAttemptEnd(ctx context.Context, r AttemptResult) {
	if c == nil || c.ended {
		return
	}
	r.Request = c.redactRequest(ctx, r.Request.Clone())
	r.Request.Body = boundedBody(r.Request.Body, c.config.Capture.RequestBodyLimit)
	if r.Response != nil {
		response := c.redactResponse(ctx, r.Response.Clone())
		response.Body = boundedBody(response.Body, c.config.Capture.ResponseBodyLimit)
		r.Response = &response
	}
	c.safe(ctx, "attempt_end", func() { c.session.OnAttemptEnd(ctx, r) })
}

func boundedBody(body BodySnapshot, limit int64) BodySnapshot {
	if int64(len(body.Data)) > limit {
		body.Data = nil
		body.OmittedReason = "redacted_body_too_large"
	}
	return body
}

// OnRetry 在执行层等待之前派发重试计划，不负责等待或决定是否重试。
func (c *Call) OnRetry(ctx context.Context, r RetryEvent) {
	if c == nil || c.ended {
		return
	}
	c.safe(ctx, "retry", func() { c.session.OnRetry(ctx, r) })
}

// OnEnd 最多派发一次结束事件；即使先前回调 panic，也会继续尝试派发结束事件。
func (c *Call) OnEnd(ctx context.Context, r CallResult) {
	if c == nil || c.ended {
		return
	}
	c.ended = true
	c.safe(ctx, "end", func() { c.session.OnEnd(ctx, r) })
}

func (c *Call) redactRequest(ctx context.Context, r RequestSnapshot) (out RequestSnapshot) {
	// 自定义脱敏器失败时整体省略，不把原始数据作为错误回退输出。
	out = RequestSnapshot{Method: r.Method, URL: "[omitted]", Body: BodySnapshot{OmittedReason: "redactor_failed"}}
	c.safe(ctx, "redact_request", func() { out = c.config.Redactor.RedactRequest(r) })
	return out
}

func (c *Call) redactResponse(ctx context.Context, r ResponseSnapshot) (out ResponseSnapshot) {
	out = ResponseSnapshot{StatusCode: r.StatusCode, Body: BodySnapshot{OmittedReason: "redactor_failed"}}
	c.safe(ctx, "redact_response", func() { out = c.config.Redactor.RedactResponse(r) })
	return out
}

func (c *Call) safe(ctx context.Context, operation string, fn func()) {
	defer func() {
		if v := recover(); v != nil {
			c.report(ctx, &CallbackError{Operation: operation, PanicType: fmt.Sprintf("%T", v)})
		}
	}()
	fn()
}

func (c *Call) report(ctx context.Context, err error) {
	// 错误报告也属于用户扩展边界，不能反过来使 HTTP 请求失败。
	defer func() { _ = recover() }()
	if c.config.OnError != nil {
		c.config.OnError(ctx, err)
		return
	}
	slog.Default().ErrorContext(ctx, "http.logging.error", "error", err.Error())
}
