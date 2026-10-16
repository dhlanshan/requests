package tn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dhlanshan/requests/internal/utils"
)

type CtxJKExtend struct{}

// Validator 保留原有签名；耗时校验请改用支持取消的 ContextValidator。
type Validator func(respBody []byte, respHeader http.Header) (bool, error)
type ContextValidator func(context.Context, []byte, http.Header) (bool, error)

// BodyFactory 每次返回独立的流；长度未知时返回 -1。
// 工厂须响应 Context，流的 Close 应能解除阻塞的 Read。
type BodyFactory func(context.Context) (io.ReadCloser, int64, error)

// BodySource 允许流式编码器重新打开 Body，无需缓存整个文件。
type BodySource interface {
	NewBody(context.Context) (io.ReadCloser, int64, error)
	Replayable() bool
}

// RetryInfo 传给 RetryDecider；Attempt 从 1 开始，Header 是独立副本。
type RetryInfo struct {
	Attempt      int
	StatusCode   int
	Header       http.Header
	Err          error
	DefaultRetry bool
}

// ApiParam 描述一次 Api/ApiContext 调用的请求内容、超时、重试、校验及日志配置。
// 时间字段使用 time.Duration，容量字段以字节为单位；各字段的零值含义见对应注释。
// 调用期间配置应保持稳定；并发调用可以共享只读配置，但不能共享同一个正在读取的 Body。
type ApiParam struct {
	// Url 是必填的完整请求地址，例如 "https://example.com/api?page=1"。
	// 仅接受带主机名的 http/https URL；禁止 user:password@host 形式的凭证，认证请放入 Header。
	// 已有查询参数会保留，Params 中的参数在其后追加；额外的目标访问限制见 URLPolicy。
	Url string `json:"url"`

	// Method 是必填的大写 HTTP 方法，没有默认值，也不会自动转换大小写。
	// 支持 GET、POST、PUT、PATCH、DELETE、HEAD、OPTIONS、TRACE、CONNECT。
	// 方法会影响是否允许重试；非 GET/HEAD/OPTIONS 请求还需满足 AllowUnsafeRetry 的说明。
	Method string `json:"method"`

	// Header 设置本次请求的单值请求头，nil 表示不添加自定义头。
	// 头名按 HTTP 规则忽略大小写，大小写不同的重复键会被拒绝；头名和头值不能包含非法控制字符。
	// Host 写入 Request.Host；Content-Length 设置请求长度，并在长度已知时检查一致性。
	// 显式声明 0 时，会在单次期限内探测一个字节确认空正文；发现内容则在发送前拒绝。
	// Transfer-Encoding 由 HTTP 层管理，不能通过此字段覆盖。
	// 使用 tc 构造 Body 时，应将构造器返回的 Content-Type 在这里显式设置。
	Header map[string]string `json:"header"`

	// Params 是追加到 Url 的查询参数，nil 或空 map 表示不追加。
	// 键和值传入原始文本，由请求层进行 URL 编码；不要预先进行百分号编码。
	// 若 Url 已有同名键，会保留原值并追加新值，例如 ?tag=a 加 tag=b 得到同名多值。
	Params map[string]string `json:"params"`

	// Body 是本次调用的请求正文，与 BodyFactory 互斥；两者都为 nil 时发送空正文。
	// 请求库接管该流的所有权：实现 io.Closer 时，成功、失败或参数检查提前返回都会负责关闭。
	// bytes.Reader、bytes.Buffer、strings.Reader 等已知类型可通过 GetBody 重放；
	// 实现 BodySource 的编码器按其能力重开流，其他一次性流需要重试时受 MaxReplayBytes 限制。
	// 大文件优先使用 BodyFactory 或可重放的 multipart 文件路径，避免整个文件进入重试缓存。
	// 调用方在执行期间不能继续读写该流；底层字节切片也应保持不变。
	Body io.Reader `json:"-"`

	// BodyFactory 按需为每次尝试创建独立正文，也可能在 307/308 重定向等需要 GetBody 时再次调用。
	// 成功时返回非 nil 的 io.ReadCloser 和准确的字节长度；长度未知返回 -1，失败返回 error。
	// 每次返回的流应具有一致的内容，并各自维护读取位置，不能反复返回同一个已消耗的流。
	// 打开过程应响应传入的 Context，流的 Close 应能解除阻塞的 Read；与 Body 互斥。
	// 适合大型流式上传，例如 tc.FileBodyFactory(path)，无需受 MaxReplayBytes 缓存容量限制。
	BodyFactory BodyFactory `json:"-"`

	// SingleTimeout 限制一次尝试，0 使用默认 30 秒，不能为负数。
	// 一次尝试包括打开 Body、HTTP 交互及重定向、读取响应和业务校验，不包含重试等待。
	// 若上游 Context 或总 Timeout 更早到期，以更早者为准；一次性流的预缓存计入总 Timeout。
	// 使用 2*time.Second 等 Duration 值；直接填写整数 2 表示 2 纳秒。
	SingleTimeout time.Duration `json:"single_timeout"`

	// Timeout 限制整次调用，覆盖 Body 准备、所有尝试、响应校验和重试等待，不能为负数。
	// 0 时自动计算：SingleTimeout*(Retry+1) + 等待预算*Retry，溢出时饱和到最大 Duration。
	// 固定间隔的等待预算取 RetryInterval 与 MaxRetryDelay 的较小值；
	// 使用默认退避或 Backoff 时，等待预算使用 MaxRetryDelay。
	// ApiContext 的上游期限更短时优先；Context 通过协作取消，自定义回调仍需及时响应取消。
	Timeout time.Duration `json:"timeout"`

	// Retry 是首次尝试以外的额外次数，默认 0，允许范围为 0..100。
	// 例如 2 表示最多执行 3 次库层面的尝试；单次内部重定向和 Transport 的透明重试另行处理。
	// 设置次数并不保证重试，还需方法允许、失败可重试、正文可重放且总期限未到。
	Retry int `json:"retry"`

	// RetryInterval 是显式重试之间的固定等待间隔，不能为负数。
	// 0 时使用带随机抖动的指数退避：指数基值 100ms，指数上限 2s，取其一半至完整间隔。
	// Backoff 非 nil 时优先使用 Backoff；服务端 Retry-After 可增大等待，最终受 MaxRetryDelay 限制。
	// 等待期间响应上游取消及总 Timeout。
	RetryInterval time.Duration `json:"retry_interval"`

	// AllowUnsafeRetry 默认 false，用于显式允许 POST/PUT/PATCH/DELETE 等方法进行重试。
	// GET/HEAD/OPTIONS 默认允许重试；其他方法也可通过非空白 Idempotency-Key 请求头放行。
	// 幂等键必须有服务端去重支持；设为 true 表示调用方接受重复操作的业务风险。
	// 重试资格覆盖重定向中的每一跳；本开关为 false 时，非安全方法跳转后失去幂等键会停止后续重试。
	// 此开关只放行方法检查，仍需满足 Retry 次数、失败分类、正文重放及超时条件。
	AllowUnsafeRetry bool `json:"allow_unsafe_retry"`

	// RetryStatusCodes 指定 HTTP 状态失败时可重试的状态码，每项必须在 100..599 范围内。
	// nil 使用默认值：408、429、500、502、503、504；非 nil 空切片表示关闭按状态码重试。
	// 这里只处理 SuccessStatus 判定为失败的状态；网络、读取及业务校验错误仍可独立触发重试。
	// 即使命中状态码，也必须满足方法允许、正文可重放、容量未超限和仍有重试预算等条件。
	RetryStatusCodes []int `json:"retry_status_codes"`

	// RetryValidation 显式允许业务校验失败后的重试，默认 false，避免重复请求永久业务失败。
	// 对于临时业务状态可设为 true；验证器仍可用 tn.NoRetry(err) 标记不可重试的错误。
	RetryValidation bool `json:"retry_validation"`
	// RetryDecider 可覆盖普通失败的默认重试判断，nil 使用内置分类。
	// 无法绕过方法安全检查、总期限、次数、容量/地址限制或 NoRetry 标记；同步回调应响应 Context。
	RetryDecider func(context.Context, RetryInfo) bool `json:"-"`

	// Backoff 自定义重试等待时间，参数从 1 开始：1 表示首次失败后、第二次尝试前的等待。
	// nil 时使用 RetryInterval；该间隔也为 0 时使用默认指数退避和随机抖动。
	// 返回负数按 0 处理；Retry-After 可以延长等待，最终结果会被 MaxRetryDelay 截断。
	// 回调同步执行，应及时返回；返回 0 只代表无需基础等待，不会绕过 Retry-After 或取消检查。
	Backoff func(int) time.Duration `json:"-"`

	// MaxRetryDelay 是单次重试等待的上限，0 默认 30 秒，不能为负数。
	// 同时约束固定间隔、自定义/默认退避和服务端 Retry-After；不限制网络执行时间。
	// 自动计算总 Timeout 时也会使用此值；实际等待仍可能被更早到期的总 Context 中断。
	MaxRetryDelay time.Duration `json:"max_retry_delay"`

	// EnableValid 控制业务校验，默认 false；关闭时仍进行 HTTP 状态检查和正文容量检查。
	// 校验在重定向结束、HTTP 状态成功且响应完整读取后执行。
	// 开启后若未提供任何验证器，默认要求 JSON 中 code == 200 且 msg == "success"。
	EnableValid bool `json:"enable_valid"`

	// Validator 是兼容原签名的同步业务验证器，仅在 EnableValid=true 时执行。
	// 仅 true,nil 表示通过；false,nil、任意非 nil error 或 panic 都视为校验失败。
	// 原始 error 会被包装保留，可通过 errors.Is/As 判断；校验失败是否重试仍受其他条件限制。
	// 与 ContextValidator 互斥；此签名无法接收取消信号，耗时校验优先使用 ContextValidator。
	Validator Validator `json:"-"`

	// ContextValidator 是支持取消的业务验证器，仅在 EnableValid=true 时执行，与 Validator 互斥。
	// Context 同时继承上游取消、总 Timeout 和 SingleTimeout，回调应主动检查 ctx.Err()/ctx.Done()。
	// 返回值含义与 Validator 一致；返回后若期限已过，即使返回 true,nil 也会按超时处理。
	// 回调同步执行，Context 取消不会强制终止一个不配合取消的阻塞回调。
	ContextValidator ContextValidator `json:"-"`

	// SuccessStatus 决定最终 HTTP 状态是否成功，nil 默认接受全部 200..299。
	// 非 nil 时完全采用回调结果，例如只接受 200，或同时接受 2xx 和 304，需要自行写全条件。
	// 返回 false 会形成 HTTPError；返回 true 后仍需通过容量检查及已启用的业务校验。
	SuccessStatus func(int) bool `json:"-"`

	// MaxResponseBytes 是最终响应正文的读取上限，0 默认 16MiB（16*1024*1024 字节）。
	// 自动解压启用时，对解压后的正文计数；不包含响应头，也不是整个进程的内存配额。
	// 超限返回 ErrResponseTooLarge，并保留上限以内的部分数据，不会把截断响应判为成功。
	// 不能为负数或 int64 最大值；需要更大响应时显式调高，0 不表示无限制。
	MaxResponseBytes int64 `json:"max_response_bytes"`

	// MaxErrorBytes 限制 HTTP 状态失败时保留的正文，0 默认 64KiB，不能为负数或 int64 最大值。
	// 实际错误正文上限取该值与 MaxResponseBytes 的较小值，正文同时可从返回值和 HTTPError.Body 获取。
	// 超限时 HTTPError.Truncated=true，且错误同时支持 errors.Is(err, ErrResponseTooLarge)。
	MaxErrorBytes int64 `json:"max_error_bytes"`

	// MaxReplayBytes 限制一次性流为显式重试而建立的内存缓存，0 默认 8MiB。
	// 仅当配置 Retry>0、方法允许重试且 Body 无法自行重开时使用；超限在首次发送前返回 ErrRequestBodyTooLarge。
	// 可通过 GetBody、BodyFactory 或可重放 BodySource 重新创建的正文不受此缓存上限限制。
	// 此值不限制单次流式上传大小；大文件应选择可重开流。不能为负数或 int64 最大值。
	MaxReplayBytes int64 `json:"max_replay_bytes"`

	// URLPolicy 附加业务地址检查，nil 表示不添加此类限制；HTTP/HTTPS 等基础检查仍然生效。
	// 检查合并 Params 后的初始 URL，以及每次实际跟随的重定向目标；返回 nil 放行，非 nil 错误拒绝。
	// 回调应仅读取 URL 并配合 Context 取消，可检查 HTTPS、端口、主机名或路径等业务条件。
	// 拒绝错误会包装 ErrTargetBlocked 和原始原因；实际 DNS/IP 检查需配合 ClientParam.NetworkPolicy。
	URLPolicy func(context.Context, *url.URL) error `json:"-"`

	// MaxRedirects 限制跟随重定向的次数，0 默认 10，允许范围 0..100。
	// 自定义 Client.CheckRedirect 仍能更早停止，但不能绕过以下默认安全检查。
	MaxRedirects int `json:"max_redirects"`
	// AllowCrossOriginRedirect 默认 false；不同协议、主机或有效端口视为跨源。
	// 显式允许后仍检查 URLPolicy/NetworkPolicy，并按 RedirectHeaderAllowlist 清理请求头。
	AllowCrossOriginRedirect bool `json:"allow_cross_origin_redirect"`
	// AllowHTTPSDowngrade 默认 false；按相邻两跳检查，包括 HTTP→HTTPS→HTTP 链路中的降级。
	// 只有明确接受明文传输风险时才允许 HTTPS 跳转 HTTP。
	// 跨源跳转还需要 AllowCrossOriginRedirect；重放正文另需 AllowCrossOriginBody。
	AllowHTTPSDowngrade bool `json:"allow_https_downgrade"`
	// AllowCrossOriginBody 默认 false；允许跨源 307/308 等重放正文时必须显式开启。
	// 该开关独立于 Retry/AllowUnsafeRetry，不能用重试配置代替重定向授权。
	AllowCrossOriginBody bool `json:"allow_cross_origin_body"`
	// RedirectHeaderAllowlist 指定跨源可携带的头；nil 默认只保留 Accept、Accept-Language、Content-Type、User-Agent。
	// 空切片表示全部移除。Cookie/Authorization 等凭证只有显式列入时才允许跨源携带。
	// CookieJar 跨源取 Cookie 也受 Cookie 是否列入的约束；白名单不能替代对目标服务的授权。
	RedirectHeaderAllowlist []string `json:"redirect_header_allowlist"`

	// Limiter 为多次调用共享的并发/缓冲容量预算；nil 使用进程内默认预算（64 个调用、256MiB）。
	// 排队在 Body 准备之前发生，响应仍被调用方持有的内存不计入已结束调用的预算。
	// 可通过 NewLimiter 创建业务预算；流式响应仅预留复制缓冲及错误正文预算。
	Limiter *Limiter `json:"-"`

	// Caller 标记调用方模块或业务名称，例如 "order.Create"，默认空字符串。
	// 仅作为日志 caller 字段，不自动写入 HTTP 请求头；应填入适合记录的非敏感信息。
	Caller string `json:"caller"`

	// EchoReq 开启请求摘要日志，默认 false；进入执行准备阶段后，在调用结束时统一记录。
	// 输出方法、origin、请求/跟踪标识及脱敏 Header，URL 路径和查询参数不会写入日志。
	// 正文预览还需要 LogBody=true；预览来源为首次尝试，日志实际是否输出取决于 Logger 的级别设置。
	EchoReq bool `json:"echo_req"`

	// EchoRes 开启最终响应摘要日志，默认 false，记录最近状态、尝试次数、耗时和脱敏 Header。
	// 失败时记录错误类型，不直接打印可能包含敏感数据的原始错误文本。
	// 正文仅在最终成功、完整且满足 LogBody 预览条件时输出；日志级别仍受 Logger 控制。
	EchoRes bool `json:"echo_res"`

	// TraceId 是业务提供的链路跟踪标识，默认空字符串，写入日志 trace_id 字段。
	// 不会自动生成或注入 HTTP 请求头；如需向下游透传，应同时在 Header 中设置业务约定的跟踪头。
	// 请求库另行生成 request_id，用于区分每次 Api 调用；它与此字段相互独立。
	TraceId string `json:"trace_id"`

	// LogBody 开启有界、脱敏的正文预览，默认 false，且只有对应 EchoReq/EchoRes 开关开启时才使用。
	// 仅支持完整的小型 JSON（含 +json）及 application/x-www-form-urlencoded，需正确设置 Content-Type。
	// 二进制、multipart、XML、纯文本、非法或不完整数据整体省略；不会打印原始片段作为回退。
	// 请求随首次发送捕获有限前缀，响应复用已读取正文；敏感字段规则见 SensitiveFields。
	LogBody bool `json:"log_body"`

	// LogBodyLimit 是单个正文预览的字节上限，0 默认 4KiB，允许正值范围为 1..65536。
	// 对原始正文长度和脱敏编码后的长度都会检查；超限或无法确认完整时输出 "[omitted]"。
	// 不会通过截取一段不完整 JSON 来绕过脱敏；此限制不影响实际发送或返回正文的长度。
	LogBodyLimit int `json:"log_body_limit"`

	// SensitiveFields 在内置 password/token/cookie/secret 等规则之外，追加需要脱敏的业务字段名。
	// nil 或空切片仍保留内置规则；自定义字段按名称完整匹配，忽略大小写及名称中的 '-'、'_'、'.'。
	// 用于 JSON 递归字段和 URL 表单键的预览脱敏；跨源请求头由 RedirectHeaderAllowlist 控制。
	// 例如 []string{"mobile", "id_card"}；字段匹配不能识别任意自然语言中的秘密，敏感业务宜关闭 LogBody。
	SensitiveFields []string `json:"sensitive_fields,omitempty"`

	// Logger 指定本次调用的结构化日志输出，nil 使用 slog.Default()。
	// 日志使用 Info 级别，只有对应 Echo 开关开启且 Handler 允许该级别时才会输出。
	// 日志同步写入，缓慢的 Handler 会增加调用返回耗时；并发共享时，Handler 应支持并发使用。
	Logger *slog.Logger `json:"-"`
}

// Check 在分配连接或读取 Body 前拒绝无效参数。
func (a *ApiParam) Check() error {
	if a.Body != nil && utils.IsNil(a.Body) {
		return errors.New("nil body reader")
	}
	u, err := url.Parse(a.Url)
	if err != nil || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("url must be an absolute http or https URL")
	}
	if u.User != nil {
		return errors.New("URL credentials are not allowed; use an Authorization header")
	}
	switch a.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE", "CONNECT":
	default:
		return errors.New("invalid method")
	}
	if a.Retry < 0 || a.Retry > 100 {
		return errors.New("retry must be between 0 and 100")
	}
	if a.MaxRedirects < 0 || a.MaxRedirects > 100 {
		return errors.New("max_redirects must be between 0 and 100")
	}
	for _, name := range a.RedirectHeaderAllowlist {
		if !validHeaderName(name) {
			return errors.New("invalid redirect header allowlist")
		}
	}
	if a.Timeout < 0 || a.SingleTimeout < 0 || a.RetryInterval < 0 || a.MaxRetryDelay < 0 {
		return errors.New("durations cannot be negative")
	}
	for name, limit := range map[string]int64{
		"max_response_bytes": a.MaxResponseBytes, "max_error_bytes": a.MaxErrorBytes, "max_replay_bytes": a.MaxReplayBytes,
	} {
		if limit < 0 || limit == int64(^uint64(0)>>1) {
			return fmt.Errorf("%s is out of range", name)
		}
	}
	if a.LogBodyLimit < 0 || a.LogBodyLimit > 64<<10 {
		return errors.New("log_body_limit must be between 0 and 65536")
	}
	if a.Body != nil && a.BodyFactory != nil {
		return errors.New("Body and BodyFactory are mutually exclusive")
	}
	if a.Validator != nil && a.ContextValidator != nil {
		return errors.New("Validator and ContextValidator are mutually exclusive")
	}
	for _, code := range a.RetryStatusCodes {
		if code < 100 || code > 599 {
			return errors.New("invalid retry status code")
		}
	}
	seen := make(map[string]bool, len(a.Header))
	for k, v := range a.Header {
		key := http.CanonicalHeaderKey(k)
		if key == "" || !validHeaderName(k) || !validHeaderValue(v) || seen[key] {
			return errors.New("invalid or duplicate request header")
		}
		seen[key] = true
	}
	return nil
}

func validHeaderName(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return name != ""
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] == 127 || (value[i] < 32 && value[i] != '\t') {
			return false
		}
	}
	return true
}
