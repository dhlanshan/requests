package runner

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dhlanshan/requests/internal/idgen"
	"github.com/dhlanshan/requests/logging"
	"github.com/dhlanshan/requests/tn"
)

// 日志协议及输出实现放在公开 logging 包；本文件只连接执行过程与该协议，
// 并在实际读流时采集有界副本。日志关闭或 Logger 拒绝本次会话时不包装正文。
func startCallLogging(ctx context.Context, p tn.ApiParam, started time.Time) *logging.Call {
	if p.Logging == nil {
		return nil
	}
	return logging.Start(logContext(ctx), p.Logging, logging.CallInfo{
		RequestID: "R" + idgen.GenKsuId(), TraceID: p.TraceId, Caller: p.Caller,
		Method: p.Method, URL: p.Url, StartedAt: started,
	})
}

// 日志必须能够描述 nil context 的输入错误；这里的 Background 仅用于日志，
// 不会把无效的 HTTP Context 悄悄替换成有效请求。
func logContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

type attemptLog struct {
	call         *logging.Call
	capture      logging.CaptureOptions
	number       int
	started      time.Time
	request      logging.RequestSnapshot
	response     *logging.ResponseSnapshot
	requestBody  *captureBody
	responseBody *captureBody
}

func newAttemptLog(call *logging.Call, template *http.Request, number int, started time.Time) *attemptLog {
	if call == nil {
		return nil
	}
	a := &attemptLog{call: call, capture: call.Capture(), number: number, started: started}
	a.request = requestSnapshot(template, a.capture.RequestHeaders)
	if a.capture.RequestBody {
		a.request.Body = logging.BodySnapshot{OmittedReason: "not_sent"}
	}
	return a
}

func requestSnapshot(req *http.Request, headers bool) logging.RequestSnapshot {
	r := logging.RequestSnapshot{
		Method: req.Method, URL: req.URL.String(), Host: req.Host,
		ContentType: req.Header.Get("Content-Type"), ContentLength: req.ContentLength,
		Body: logging.BodySnapshot{OmittedReason: "disabled"},
	}
	if headers {
		r.Header = req.Header.Clone()
	}
	return r
}

// attachRequest 在 Client.Do 之前记录业务可见的请求，并包装本次尝试自己的流。
// 它不调用 GetBody，不预读，也不对重定向内部产生的其他上传流重复采集。
func (a *attemptLog) attachRequest(req *http.Request) {
	if a == nil {
		return
	}
	a.request = requestSnapshot(req, a.capture.RequestHeaders)
	if !a.capture.RequestBody {
		return
	}
	if req.Body == nil || req.Body == http.NoBody {
		a.request.Body = logging.BodySnapshot{Complete: true}
		return
	}
	length := req.ContentLength
	if length == 0 {
		length = -1 // 非 nil 请求正文的长度 0 在 net/http 中表示未知长度。
	}
	a.requestBody = &captureBody{ReadCloser: req.Body, limit: a.capture.RequestBodyLimit, length: length}
	req.Body = a.requestBody
}

// attachResponse 在执行层读取响应前包装流，因此 ApiTo 的流式输出也能采集，
// 不需要把整个响应缓存到内存。失败响应与成功响应采用相同的采集规则。
func (a *attemptLog) attachResponse(req *http.Request, resp *http.Response) {
	if a == nil {
		return
	}
	url := req.URL.String()
	if resp.Request != nil && resp.Request.URL != nil {
		url = resp.Request.URL.String()
	}
	a.response = &logging.ResponseSnapshot{
		URL: url, StatusCode: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"),
		ContentLength: resp.ContentLength, Body: logging.BodySnapshot{OmittedReason: "disabled"},
	}
	if a.capture.ResponseHeaders {
		a.response.Header = resp.Header.Clone()
	}
	if !a.capture.ResponseBody {
		return
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		a.response.Body = logging.BodySnapshot{Complete: true}
		return
	}
	length := resp.ContentLength
	if length == 0 {
		length = -1
	}
	a.responseBody = &captureBody{ReadCloser: resp.Body, limit: a.capture.ResponseBodyLimit, length: length}
	resp.Body = a.responseBody
}

func (a *attemptLog) end(ctx context.Context, phase logging.Phase, err error) {
	if a == nil {
		return
	}
	if a.requestBody != nil {
		a.request.Body = a.requestBody.snapshot()
	}
	if a.responseBody != nil {
		a.response.Body = a.responseBody.snapshot()
	}
	a.call.OnAttemptEnd(logContext(ctx), logging.AttemptResult{
		Attempt: a.number, StartedAt: a.started, Duration: time.Since(a.started),
		Phase: phase, Request: a.request, Response: a.response, Err: err,
	})
}

// captureBody 只复制底层 Read 已经返回的字节，不为日志探测额外数据。
// Read 与 Close 可以由 Transport/取消回调并发调用，互斥锁仅保护采集状态。
// 正文读取和关闭的原始行为保持不变，底层关闭幂等仍由 lz.Guard 保证。
type captureBody struct {
	io.ReadCloser
	mu       sync.Mutex
	data     []byte
	limit    int64
	length   int64
	read     int64
	complete bool
}

func (c *captureBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.read += int64(n)
	if remaining := c.limit - int64(len(c.data)); remaining > 0 {
		count := int64(n)
		if count > remaining {
			count = remaining
		}
		c.data = append(c.data, p[:int(count)]...)
	}
	if err == io.EOF {
		c.complete = true
	}
	return n, err
}

func (c *captureBody) snapshot() logging.BodySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return logging.BodySnapshot{
		Data: c.data, Truncated: c.read > c.limit,
		Complete: c.complete || (c.length >= 0 && c.read == c.length),
	}
}
