// Package requests 示例集合。
//
// 本文件为 scenarios_test.go 中每个场景提供对应的可运行示例，
// 演示 requests 库的典型用法。示例使用 httptest.Server 或自定义 Transport，
// 不依赖外部网络，可直接通过 `go test` 运行。
//
// 示例按场景分类组织，与 scenarios_test.go 一一对应：
//  1. HTTP 方法与基础请求
//  2. Body 构造方式（tc 包）
//  3. URL 参数与请求头
//  4. 超时与取消
//  5. 重试策略
//  6. 业务校验
//  7. 重定向安全
//  8. 错误处理与诊断
//  9. 安全策略（URLPolicy / NetworkPolicy）
//
// 10. 共享预算 Limiter
// 11. 流式输出 ApiTo
// 12. 客户端管理
// 13. 日志脱敏
// 14. 压缩响应
// 15. HTTP/2 与 TLS
// 16. 中间件
// 17. 参数校验
package requests

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/dhlanshan/requests/interfaces"
	"github.com/dhlanshan/requests/tc"
	"github.com/dhlanshan/requests/tn"
)

// =====================================================================
// 1. HTTP 方法与基础请求
// =====================================================================

// Example_scenarios_allHttpMethods 演示使用不同的 HTTP 方法发送请求。
// Method 必填且不会自动转换大小写，需传入大写方法名。
func Example_scenarios_allHttpMethods() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		_, _, _ = Api(nil, tn.ApiParam{Url: server.URL, Method: method})
	}
	// Output:
	// GET
	// POST
	// PUT
	// PATCH
	// DELETE
}

// Example_scenarios_successStatuses 演示默认 2xx 全部判定为成功，
// 并能完整返回 Body 与 Header。
func Example_scenarios_successStatuses() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		_, _ = io.WriteString(w, "created")
	}))
	defer server.Close()

	body, header, err := Api(nil, tn.ApiParam{Url: server.URL, Method: "GET"})
	fmt.Printf("body=%q ct=%s err=%v\n", string(body), header.Get("Content-Type"), err)
	// Output: body="created" ct=text/plain; charset=utf-8 err=<nil>
}

// Example_scenarios_defaultClientNil 演示 client=nil 时使用进程内默认客户端，
// 默认客户端具备连接池和默认超时。
func Example_scenarios_defaultClientNil() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	body, _, err := Api(nil, tn.ApiParam{Url: server.URL, Method: "GET"})
	fmt.Printf("body=%q err=%v\n", string(body), err)
	// Output: body="ok" err=<nil>
}

// Example_scenarios_customSuccessStatus 演示 SuccessStatus 回调完全替换默认 2xx 判定，
// 例如同时接受 200 与 304。
func Example_scenarios_customSuccessStatus() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cached") == "1" {
			w.WriteHeader(304)
			return
		}
		_, _ = io.WriteString(w, "fresh")
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url:    server.URL + "?cached=1",
		Method: "GET",
		SuccessStatus: func(code int) bool {
			return code == 200 || code == 304
		},
	}
	_, _, err := Api(nil, p)
	fmt.Printf("cached err=%v\n", err)
	// Output: cached err=<nil>
}

// =====================================================================
// 2. Body 构造方式（tc 包）
// =====================================================================

// Example_scenarios_bodyJSON 演示用 tc.ContentByJson 构造 JSON 请求体，
// 返回的 Content-Type 需显式写入 Header。
func Example_scenarios_bodyJSON() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		fmt.Printf("ct=%s body=%s\n", r.Header.Get("Content-Type"), string(data))
	}))
	defer server.Close()

	payload, _ := json.Marshal(map[string]any{"k": "v"})
	reader, ct, _ := tc.ContentByJson(payload)
	_, _, _ = Api(nil, tn.ApiParam{
		Url: server.URL, Method: "POST", Body: reader,
		Header: map[string]string{"Content-Type": ct},
	})
	// Output: ct=application/json body={"k":"v"}
}

// Example_scenarios_bodyXML 演示用 tc.ContentByXml 构造 XML 请求体。
func Example_scenarios_bodyXML() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		fmt.Printf("ct=%s body=%s\n", r.Header.Get("Content-Type"), string(data))
	}))
	defer server.Close()

	payload := []byte("<root><k>v</k></root>")
	reader, ct, _ := tc.ContentByXml(payload)
	_, _, _ = Api(nil, tn.ApiParam{
		Url: server.URL, Method: "POST", Body: reader,
		Header: map[string]string{"Content-Type": ct},
	})
	// Output: ct=application/xml body=<root><k>v</k></root>
}

// Example_scenarios_bodyRaw 演示用 tc.ContentByRaw 构造纯文本请求体。
func Example_scenarios_bodyRaw() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		fmt.Printf("ct=%s body=%s\n", r.Header.Get("Content-Type"), string(data))
	}))
	defer server.Close()

	reader, ct, _ := tc.ContentByRaw("plain text body")
	_, _, _ = Api(nil, tn.ApiParam{
		Url: server.URL, Method: "POST", Body: reader,
		Header: map[string]string{"Content-Type": ct},
	})
	// Output: ct=text/plain body=plain text body
}

// Example_scenarios_bodyBinary 演示用 tc.ContentByBinary 上传二进制字节切片。
func Example_scenarios_bodyBinary() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		fmt.Printf("ct=%s n=%d\n", r.Header.Get("Content-Type"), len(data))
	}))
	defer server.Close()

	payload := []byte{0x00, 0x01, 0x02, 0xFF}
	reader, _, ct, _ := tc.ContentByBinary(payload)
	_, _, _ = Api(nil, tn.ApiParam{
		Url: server.URL, Method: "POST", Body: reader,
		Header: map[string]string{"Content-Type": ct},
	})
	// Output: ct=application/octet-stream n=4
}

// Example_scenarios_bodyMsgpack 演示用 tc.ContentByMsgpack 编码 MsgPack 请求体。
func Example_scenarios_bodyMsgpack() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		fmt.Printf("ct=%s n=%d\n", r.Header.Get("Content-Type"), len(data))
	}))
	defer server.Close()

	reader, ct, _ := tc.ContentByMsgpack(map[string]any{"k": "v"})
	_, _, _ = Api(nil, tn.ApiParam{
		Url: server.URL, Method: "POST", Body: reader,
		Header: map[string]string{"Content-Type": ct},
	})
	// Output: ct=application/x-msgpack n=5
}

// Example_scenarios_bodyFormUrlencoded 演示用 tc.ContentByXWFormUrlencoded 构造表单。
func Example_scenarios_bodyFormUrlencoded() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fmt.Printf("a=%s b=%s\n", r.FormValue("a"), r.FormValue("b"))
	}))
	defer server.Close()

	reader, ct, _ := tc.ContentByXWFormUrlencoded(map[string]any{"a": "1", "b": "two"})
	_, _, _ = Api(nil, tn.ApiParam{
		Url: server.URL, Method: "POST", Body: reader,
		Header: map[string]string{"Content-Type": ct},
	})
	// Output: a=1 b=two
}

// Example_scenarios_bodyMultipartBytes 演示用 tc.ContentByFormData 上传 multipart 文件。
func Example_scenarios_bodyMultipartBytes() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 16)
		file, _, _ := r.FormFile("file")
		data, _ := io.ReadAll(file)
		file.Close()
		fmt.Printf("file=%s desc=%s\n", string(data), r.FormValue("desc"))
	}))
	defer server.Close()

	reader, ct, _ := tc.ContentByFormData(map[string]any{
		"file": tc.FormFile{Filename: "f.bin", Content: []byte("hello")},
		"desc": "desc-value",
	})
	_, _, _ = Api(nil, tn.ApiParam{
		Url: server.URL, Method: "POST", Body: reader,
		Header: map[string]string{"Content-Type": ct},
	})
	// Output: file=hello desc=desc-value
}

// Example_scenarios_bodyFactoryReopensForRetry 演示 BodyFactory 在重试时重新打开独立流，
// 适合大型流式上传，无需将整个文件缓存到内存。
func Example_scenarios_bodyFactoryReopensForRetry() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		fmt.Printf("body=%s\n", string(data))
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "POST",
		Retry: 1, AllowUnsafeRetry: true,
		RetryInterval: time.Nanosecond,
	}
	p.BodyFactory = func(context.Context) (io.ReadCloser, int64, error) {
		return io.NopCloser(strings.NewReader("payload")), 7, nil
	}
	_, _, _ = Api(nil, p)
	// Output: body=payload
}

// =====================================================================
// 3. URL 参数与请求头
// =====================================================================

// Example_scenarios_queryParamsAppend 演示 Params 在 Url 已有查询参数之后追加，
// 同名键会形成多值，原始值不被覆盖。
func Example_scenarios_queryParamsAppend() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.URL.RawQuery)
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url:    server.URL + "?tag=a",
		Method: "GET",
		Params: map[string]string{"tag": "b", "page": "1"},
	}
	_, _, _ = Api(nil, p)
	// Output: page=1&tag=a&tag=b
}

// Example_scenarios_hostHeader 演示 Host 头写入 Request.Host，
// 用于虚拟主机场景。
func Example_scenarios_hostHeader() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Host)
	}))
	defer server.Close()

	// 用 server 的 IP 端口，但 Host 头设为虚拟主机名。
	p := tn.ApiParam{
		Url:    server.URL,
		Method: "GET",
		Header: map[string]string{"Host": "virtual.example"},
	}
	_, _, _ = Api(nil, p)
	// Output: virtual.example
}

// Example_scenarios_cookieJar 演示配置 Jar 后跨请求自动携带 Cookie。
func Example_scenarios_cookieJar() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "token", Value: "abc", Path: "/"})
			return
		}
		if c, err := r.Cookie("token"); err == nil {
			fmt.Println(c.Value)
		}
	}))
	defer server.Close()

	jar, _ := cookiejar.New(nil)
	client, _ := NewClient(tn.ClientParam{SpaceName: "jar-example", Jar: jar})
	p := tn.ApiParam{Url: server.URL + "/set", Method: "GET"}
	_, _, _ = Api(client, p)
	p.Url = server.URL + "/get"
	_, _, _ = Api(client, p)
	// Output: abc
}

// =====================================================================
// 4. 超时与取消
// =====================================================================

// Example_scenarios_singleTimeout 演示 SingleTimeout 限制单次尝试，
// 超时后表现为 context.DeadlineExceeded；总 Context 仍允许下一次重试。
func Example_scenarios_singleTimeout() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // 故意阻塞直到单次超时
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "GET",
		SingleTimeout: 20 * time.Millisecond,
		Timeout:       time.Second,
		Retry:         1,
	}
	_, _, err := Api(nil, p)
	fmt.Println(errors.Is(err, context.DeadlineExceeded))
	// Output: true
}

// Example_scenarios_totalTimeout 演示 Timeout 覆盖整次调用（含所有重试等待），
// 一旦到期立即停止后续尝试。
func Example_scenarios_totalTimeout() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	p := tn.ApiParam{
		Url: server.URL, Method: "GET",
		Retry:         10,
		RetryInterval: 50 * time.Millisecond,
	}
	_, _, err := ApiContext(ctx, nil, p)
	fmt.Println(errors.Is(err, context.DeadlineExceeded))
	// Output: true
}

// Example_scenarios_parentCancel 演示上游 Context 取消时立即返回 context.Canceled，
// 不会发出请求。
func Example_scenarios_parentCancel() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := ApiContext(ctx, nil, tn.ApiParam{Url: server.URL, Method: "GET"})
	fmt.Println(errors.Is(err, context.Canceled))
	// Output: true
}

// =====================================================================
// 5. 重试策略
// =====================================================================

// Example_scenarios_getRetriesByDefault 演示 GET 默认允许重试，
// 命中默认可重试状态码（如 503）时会按 Retry 次数重试。
func Example_scenarios_getRetriesByDefault() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	p := tn.ApiParam{Url: server.URL, Method: "GET", Retry: 2, RetryInterval: time.Nanosecond}
	body, _, err := Api(nil, p)
	fmt.Printf("calls=%d body=%q err=%v\n", calls, string(body), err)
	// Output: calls=2 body="ok" err=<nil>
}

// Example_scenarios_postNotRetriedByDefault 演示 POST 默认不重试，
// 即使状态码可重试也只执行一次。
func Example_scenarios_postNotRetriedByDefault() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "POST", Retry: 2,
		Body: strings.NewReader("x"),
	}
	_, _, err := Api(nil, p)
	fmt.Printf("calls=%d err=%v\n", calls, err != nil)
	// Output: calls=1 err=true
}

// Example_scenarios_postOptInUnsafeRetry 演示 AllowUnsafeRetry=true 显式放行
// POST 等非安全方法的重试，调用方接受重复操作的业务风险。
func Example_scenarios_postOptInUnsafeRetry() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "POST", Retry: 2,
		AllowUnsafeRetry: true, RetryInterval: time.Nanosecond,
		Body: strings.NewReader("x"),
	}
	body, _, err := Api(nil, p)
	fmt.Printf("calls=%d body=%q err=%v\n", calls, string(body), err)
	// Output: calls=2 body="ok" err=<nil>
}

// Example_scenarios_idempotencyKeyEnablesRetry 演示非空 Idempotency-Key 请求头
// 可以放行 POST 等非安全方法的重试（依赖服务端去重）。
func Example_scenarios_idempotencyKeyEnablesRetry() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "POST", Retry: 2,
		RetryInterval: time.Nanosecond,
		Body:          strings.NewReader("x"),
		Header:        map[string]string{"Idempotency-Key": "operation-1"},
	}
	body, _, err := Api(nil, p)
	fmt.Printf("calls=%d body=%q err=%v\n", calls, string(body), err)
	// Output: calls=2 body="ok" err=<nil>
}

// Example_scenarios_customRetryStatusCodes 演示 RetryStatusCodes 非 nil 时完全替换默认集合，
// 例如只重试 500。
func Example_scenarios_customRetryStatusCodes() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(500)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "GET", Retry: 1,
		RetryInterval:    time.Nanosecond,
		RetryStatusCodes: []int{500},
	}
	body, _, err := Api(nil, p)
	fmt.Printf("calls=%d body=%q err=%v\n", calls, string(body), err)
	// Output: calls=2 body="ok" err=<nil>
}

// Example_scenarios_emptyRetryStatusCodesDisables 演示非 nil 空切片关闭状态码重试，
// 与 nil（使用默认）严格区分。
func Example_scenarios_emptyRetryStatusCodesDisables() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "GET", Retry: 2,
		RetryStatusCodes: []int{},
	}
	_, _, _ = Api(nil, p)
	fmt.Println(calls)
	// Output: 1
}

// Example_scenarios_retryDeciderOverride 演示 RetryDecider 自定义重试判断，
// 例如对 400 这种默认不可重试的状态码也强制重试。
func Example_scenarios_retryDeciderOverride() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(400)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "GET", Retry: 2,
		RetryInterval: time.Nanosecond,
		RetryDecider:  func(context.Context, tn.RetryInfo) bool { return true },
	}
	body, _, err := Api(nil, p)
	fmt.Printf("calls=%d body=%q err=%v\n", calls, string(body), err)
	// Output: calls=3 body="ok" err=<nil>
}

// Example_scenarios_retryAfterHeaderHonored 演示服务端 Retry-After 头（秒数）
// 会延长重试等待，但仍受 MaxRetryDelay 限制。
func Example_scenarios_retryAfterHeaderHonored() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "9999")
		w.WriteHeader(503)
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "GET", Retry: 1,
		MaxRetryDelay: 20 * time.Millisecond,
	}
	start := time.Now()
	_, _, _ = Api(nil, p)
	fmt.Println(time.Since(start) < 500*time.Millisecond)
	// Output: true
}

// Example_scenarios_backoffFunction 演示自定义 Backoff 函数优先于 RetryInterval 和默认指数退避。
func Example_scenarios_backoffFunction() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "GET", Retry: 2,
		Backoff: func(retry int) time.Duration { return time.Nanosecond },
	}
	body, _, err := Api(nil, p)
	fmt.Printf("calls=%d body=%q err=%v\n", calls, string(body), err)
	// Output: calls=3 body="ok" err=<nil>
}

// =====================================================================
// 6. 业务校验
// =====================================================================

// Example_scenarios_defaultValidator 演示开启 EnableValid 但不提供验证器时，
// 默认要求 JSON 中 code == 200 且 msg == "success"。
func Example_scenarios_defaultValidator() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":200,"msg":"success"}`)
	}))
	defer server.Close()

	p := tn.ApiParam{Url: server.URL, Method: "GET", EnableValid: true}
	_, _, err := Api(nil, p)
	fmt.Println(err)
	// Output: <nil>
}

// Example_scenarios_customValidator 演示自定义 Validator 的返回值含义：
// 仅 (true, nil) 表示通过；其他组合都视为校验失败。
func Example_scenarios_customValidator() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"pending"}`)
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "GET", EnableValid: true,
		Validator: func(_ context.Context, body []byte, _ http.Header) (bool, error) {
			var v struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal(body, &v)
			if v.Status != "ready" {
				return false, nil // 业务未就绪
			}
			return true, nil
		},
	}
	_, _, err := Api(nil, p)
	fmt.Println(errors.Is(err, tn.ErrValidation))
	// Output: true
}

// Example_scenarios_validatorCancellation 演示 Validator 可接收取消信号，
// 适合耗时的业务校验场景。
func Example_scenarios_validatorCancellation() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "GET", EnableValid: true,
		SingleTimeout: 20 * time.Millisecond,
		Validator: func(ctx context.Context, _ []byte, _ http.Header) (bool, error) {
			<-ctx.Done() // 模拟阻塞校验
			return true, nil
		},
	}
	_, _, err := Api(nil, p)
	fmt.Println(errors.Is(err, context.DeadlineExceeded))
	// Output: true
}

// Example_scenarios_validationRetryEnabled 演示 RetryValidation=true 允许校验失败重试，
// 适合临时业务状态（如等待异步任务完成）。
func Example_scenarios_validationRetryEnabled() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"status":"pending"}`)
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "GET", Retry: 2,
		EnableValid: true, RetryValidation: true,
		RetryInterval: time.Nanosecond,
		Validator:     func(context.Context, []byte, http.Header) (bool, error) { return false, nil },
	}
	_, _, _ = Api(nil, p)
	fmt.Println(calls)
	// Output: 3
}

// Example_scenarios_validationNoRetryMark 演示 tn.NoRetry 标记的校验错误
// 即使 RetryValidation=true 也不会重试，用于永久业务失败。
func Example_scenarios_validationNoRetryMark() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"error":"invalid_input"}`)
	}))
	defer server.Close()

	permanent := errors.New("invalid input")
	p := tn.ApiParam{
		Url: server.URL, Method: "GET", Retry: 2,
		EnableValid: true, RetryValidation: true,
		Validator: func(context.Context, []byte, http.Header) (bool, error) { return false, tn.NoRetry(permanent) },
	}
	_, _, err := Api(nil, p)
	fmt.Printf("calls=%d permanent=%v\n", calls, errors.Is(err, permanent))
	// Output: calls=1 permanent=true
}

// =====================================================================
// 7. 重定向安全
// =====================================================================

// Example_scenarios_sameOriginRedirect 演示同源重定向默认允许，
// 请求头与正文都会被保留并重发。
func Example_scenarios_sameOriginRedirect() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusTemporaryRedirect)
			return
		}
		body, _ := io.ReadAll(r.Body)
		fmt.Printf("body=%s header=%s\n", string(body), r.Header.Get("X-Test"))
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL + "/start", Method: "POST",
		Body:   strings.NewReader("payload"),
		Header: map[string]string{"X-Test": "yes"},
	}
	_, _, _ = Api(server.Client(), p)
	// Output: body=payload header=yes
}

// Example_scenarios_crossOriginRedirectDeniedByDefault 演示跨源重定向默认被拒绝，
// 不会发出对端请求。
func Example_scenarios_crossOriginRedirectDeniedByDefault() {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("target reached")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	_, _, err := Api(source.Client(), tn.ApiParam{Url: source.URL, Method: "GET"})
	fmt.Println(errors.Is(err, tn.ErrTargetBlocked))
	// Output: true
}

// Example_scenarios_crossOriginRedirectStripsSecrets 演示显式允许跨源跳转后，
// 仅保留白名单内的请求头，Authorization/Cookie 等凭证被清理。
func Example_scenarios_crossOriginRedirectStripsSecrets() {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("auth=%s cookie=%s accept=%s\n",
			r.Header.Get("Authorization"), r.Header.Get("Cookie"), r.Header.Get("Accept"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	p := tn.ApiParam{
		Url: source.URL, Method: "GET",
		AllowCrossOriginRedirect: true,
		Header: map[string]string{
			"Authorization": "secret", "Cookie": "a=b", "Accept": "application/json",
		},
	}
	_, _, _ = Api(source.Client(), p)
	// Output: auth= cookie= accept=application/json
}

// Example_scenarios_httpsDowngradeDenied 演示 HTTPS→HTTP 降级默认被拒绝，
// 即使已允许跨源跳转。
func Example_scenarios_httpsDowngradeDenied() {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		res := response(302, "")
		switch req.URL.Path {
		case "/start":
			res.Header.Set("Location", "https://service.test/secure")
		case "/secure":
			res.Header.Set("Location", "http://service.test/final")
		}
		return res, nil
	})}
	p := tn.ApiParam{
		Url: "https://service.test/start", Method: "GET",
		AllowCrossOriginRedirect: true,
	}
	_, _, err := Api(client, p)
	fmt.Println(errors.Is(err, tn.ErrTargetBlocked))
	// Output: true
}

// Example_scenarios_crossOriginBodyReplayAllowed 演示 AllowCrossOriginBody=true
// 放行跨源 307/308 正文重放（默认拒绝）。
func Example_scenarios_crossOriginBodyReplayAllowed() {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "source.test" {
			res := response(307, "")
			res.Header.Set("Location", "https://other.test/next")
			return res, nil
		}
		data, _ := io.ReadAll(req.Body)
		fmt.Println(string(data))
		return response(200, "ok"), nil
	})}
	p := tn.ApiParam{
		Url: "https://source.test/start", Method: "POST",
		Body:                     strings.NewReader("private-body"),
		AllowCrossOriginRedirect: true,
		AllowCrossOriginBody:     true,
	}
	_, _, _ = Api(client, p)
	// Output: private-body
}

// Example_scenarios_maxRedirectsExceeded 演示 MaxRedirects 限制跳转次数，
// 超出后返回 ErrTargetBlocked。
func Example_scenarios_maxRedirectsExceeded() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound) // 无限自跳
	}))
	defer server.Close()

	p := tn.ApiParam{Url: server.URL, Method: "GET", MaxRedirects: 2}
	_, _, err := Api(server.Client(), p)
	fmt.Println(errors.Is(err, tn.ErrTargetBlocked))
	// Output: true
}

// =====================================================================
// 8. 错误处理与诊断
// =====================================================================

// Example_scenarios_httpErrorPreservesBody 演示 HTTP 状态失败时返回 *tn.HTTPError，
// 保留有界 Body 与 Header，便于处理业务错误响应。
func Example_scenarios_httpErrorPreservesBody() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"code":400,"msg":"bad"}`)
	}))
	defer server.Close()

	body, _, err := Api(nil, tn.ApiParam{Url: server.URL, Method: "GET"})
	var httpErr *tn.HTTPError
	errors.As(err, &httpErr)
	fmt.Printf("status=%d body=%s\n", httpErr.StatusCode, string(body))
	// Output: status=400 body={"code":400,"msg":"bad"}
}

// Example_scenarios_maxErrorBytesTruncates 演示 MaxErrorBytes 限制错误正文保留长度，
// 超限时 HTTPError.Truncated=true。
func Example_scenarios_maxErrorBytesTruncates() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, "abcdef")
	}))
	defer server.Close()

	p := tn.ApiParam{Url: server.URL, Method: "GET", MaxErrorBytes: 4}
	body, _, err := Api(nil, p)
	var httpErr *tn.HTTPError
	errors.As(err, &httpErr)
	fmt.Printf("truncated=%v body=%s\n", httpErr.Truncated, string(body))
	// Output: truncated=true body=abcd
}

// Example_scenarios_maxResponseBytesTruncates 演示 MaxResponseBytes 限制成功响应正文，
// 超限返回 ErrResponseTooLarge。
func Example_scenarios_maxResponseBytesTruncates() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "abcdef")
	}))
	defer server.Close()

	p := tn.ApiParam{Url: server.URL, Method: "GET", MaxResponseBytes: 3}
	body, _, err := Api(nil, p)
	fmt.Printf("err=%v body=%s\n", errors.Is(err, tn.ErrResponseTooLarge), string(body))
	// Output: err=true body=abc
}

// Example_scenarios_networkErrorOmitsURL 演示网络错误的展示文本不包含 URL 或查询参数中的敏感信息，
// 但通过 Unwrap 仍可获取原始错误用于诊断。
func Example_scenarios_networkErrorOmitsURL() {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	_, _, err := Api(client, tn.ApiParam{
		Url: "https://example.test/private?token=secret", Method: "GET",
	})
	fmt.Println(strings.Contains(err.Error(), "secret"))
	// Output: false
}

// =====================================================================
// 9. 安全策略（URLPolicy / NetworkPolicy）
// =====================================================================

// Example_scenarios_urlPolicyBlocksInitialURL 演示 URLPolicy 在首次发送前检查 URL，
// 返回 ErrTargetBlocked 包装原始原因。
func Example_scenarios_urlPolicyBlocksInitialURL() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("reached")
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL + "/forbidden", Method: "GET",
		URLPolicy: func(_ context.Context, u *url.URL) error {
			if strings.Contains(u.Path, "forbidden") {
				return errors.New("forbidden path")
			}
			return nil
		},
	}
	_, _, err := Api(nil, p)
	fmt.Println(errors.Is(err, tn.ErrTargetBlocked))
	// Output: true
}

// Example_scenarios_networkPolicyBlockPrivateIPs 演示 NewPublicClient 强制检查实际拨号 IP，
// 拒绝访问内网地址。
func Example_scenarios_networkPolicyBlockPrivateIPs() {
	client, _ := NewPublicClient(tn.ClientParam{SpaceName: "public-example"})
	_, _, err := Api(client, tn.ApiParam{Url: "http://127.0.0.1:1/private", Method: "GET"})
	fmt.Println(errors.Is(err, tn.ErrTargetBlocked))
	// Output: true
}

// Example_scenarios_networkPolicyAllowedHosts 演示 AllowedHosts 进一步限制目标主机名，
// 不在白名单内的请求被拒绝。
func Example_scenarios_networkPolicyAllowedHosts() {
	client, _ := NewPublicClient(tn.ClientParam{
		SpaceName:     "allowed-example",
		NetworkPolicy: &tn.NetworkPolicy{AllowedHosts: []string{"allowed.example"}},
	})
	_, _, err := Api(client, tn.ApiParam{Url: "http://denied.example/path", Method: "GET"})
	fmt.Println(errors.Is(err, tn.ErrTargetBlocked))
	// Output: true
}

// Example_scenarios_urlCredentialsRejected 演示 URL 中嵌入的 user:password 凭证被拒绝，
// 认证应放入 Authorization 头。
func Example_scenarios_urlCredentialsRejected() {
	_, _, err := Api(nil, tn.ApiParam{
		Url: "http://user:secret@example.test/path", Method: "GET",
	})
	fmt.Println(err != nil)
	// Output: true
}

// Example_scenarios_headerInjectionRejected 演示请求头值中嵌入的 CRLF 字符被拒绝，
// 防止头注入攻击。
func Example_scenarios_headerInjectionRejected() {
	_, _, err := Api(nil, tn.ApiParam{
		Url:    "http://example.test/path",
		Method: "GET",
		Header: map[string]string{"X-Test": "x\r\nInjected:yes"},
	})
	fmt.Println(err != nil)
	// Output: true
}

// =====================================================================
// 10. 共享预算 Limiter
// =====================================================================

// Example_scenarios_limiterBoundsConcurrency 演示 Limiter 限制在途调用数，
// 超出 maxConcurrent 的请求会排队等待。
func Example_scenarios_limiterBoundsConcurrency() {
	limiter, _ := tn.NewLimiter(2, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	// 同时发起 5 个请求，Limiter=2 保证最多 2 个在途。
	for i := 0; i < 5; i++ {
		go func() {
			p := tn.ApiParam{Url: server.URL, Method: "GET", Limiter: limiter}
			_, _, _ = Api(nil, p)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	fmt.Println("submitted")
	// Output: submitted
}

// Example_scenarios_limiterAdmissionTooLarge 演示单次预算超过 Limiter 容量时直接拒绝，
// 不进入排队。
func Example_scenarios_limiterAdmissionTooLarge() {
	limiter, _ := tn.NewLimiter(1, 1) // 缓冲容量仅 1 字节
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "GET", Limiter: limiter,
		MaxResponseBytes: 1024, // 单次预算远超容量
	}
	_, _, err := Api(nil, p)
	fmt.Println(errors.Is(err, tn.ErrAdmissionTooLarge))
	// Output: true
}

// Example_scenarios_setDefaultLimiter 演示 SetDefaultLimiter 替换后续调用的共享预算。
func Example_scenarios_setDefaultLimiter() {
	custom, _ := tn.NewLimiter(8, 32<<20)
	_ = SetDefaultLimiter(custom)
	// 后续 client=nil 且 Limiter=nil 的调用将使用 custom。
	fmt.Println("default limiter replaced")
	// Output: default limiter replaced
}

// =====================================================================
// 11. 流式输出 ApiTo
// =====================================================================

// Example_scenarios_apiToWritesSuccessResponse 演示 ApiTo 将成功响应流式写入 dst，
// 返回实际写入字节数，适合大文件下载。
func Example_scenarios_apiToWritesSuccessResponse() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	var dst bytes.Buffer
	n, _, err := ApiTo(context.Background(), nil, tn.ApiParam{Url: server.URL, Method: "GET"}, &dst)
	fmt.Printf("n=%d dst=%s err=%v\n", n, dst.String(), err)
	// Output: n=2 dst=ok err=<nil>
}

// Example_scenarios_apiToRetriesBeforeWriting 演示 ApiTo 在写入任何字节前可重试，
// 例如首次返回 503 时会再次尝试。
func Example_scenarios_apiToRetriesBeforeWriting() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	var dst bytes.Buffer
	p := tn.ApiParam{Url: server.URL, Method: "GET", Retry: 2, RetryInterval: time.Nanosecond}
	n, _, err := ApiTo(context.Background(), nil, p, &dst)
	fmt.Printf("calls=%d n=%d dst=%s err=%v\n", calls, n, dst.String(), err)
	// Output: calls=2 n=2 dst=ok err=<nil>
}

// Example_scenarios_apiToNoRetryAfterWrite 演示一旦写入任何字节便不再重试，
// 调用方负责文件/事务的提交或回滚。
func Example_scenarios_apiToNoRetryAfterWrite() {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, "ok") // 第一次就成功并写入
	}))
	defer server.Close()

	var dst bytes.Buffer
	p := tn.ApiParam{Url: server.URL, Method: "GET", Retry: 2}
	_, _, _ = ApiTo(context.Background(), nil, p, &dst)
	fmt.Printf("calls=%d dst=%s\n", calls, dst.String())
	// Output: calls=1 dst=ok
}

// =====================================================================
// 12. 客户端管理
// =====================================================================

// Example_scenarios_namedClientReuse 演示 NewClient 创建命名客户端，
// 同名且配置相符时复用已有实例。
func Example_scenarios_namedClientReuse() {
	first, _ := NewClient(tn.ClientParam{SpaceName: "reuse-example", Timeout: time.Second})
	second, _ := NewClient(tn.ClientParam{SpaceName: "reuse-example", Timeout: time.Second})
	fmt.Println(first == second)
	// Output: true
}

// Example_scenarios_namedClientConflictRejected 演示同名但配置不同的请求被拒绝，
// 避免配置冲突时静默复用错误实例。
func Example_scenarios_namedClientConflictRejected() {
	_, _ = NewClient(tn.ClientParam{SpaceName: "conflict-example", Timeout: time.Second})
	_, err := NewClient(tn.ClientParam{SpaceName: "conflict-example", Timeout: 2 * time.Second})
	fmt.Println(errors.Is(err, tn.ErrClientConfig))
	// Output: true
}

// Example_scenarios_getClientExplicitLookup 演示 GetClient 显式取得已创建的命名客户端，
// 不存在时返回 ok=false。
func Example_scenarios_getClientExplicitLookup() {
	_, _ = NewClient(tn.ClientParam{SpaceName: "lookup-example", Timeout: time.Second})
	client, ok := GetClient("lookup-example")
	fmt.Println(ok, client != nil)
	// Output: true true
}

// Example_scenarios_clientResetReplaces 演示 Reset=true 替换命名客户端，
// 关闭旧连接且不中断在途请求。
func Example_scenarios_clientResetReplaces() {
	first, _ := NewClient(tn.ClientParam{SpaceName: "reset-example", Timeout: time.Second})
	second, _ := NewClient(tn.ClientParam{
		SpaceName: "reset-example", Timeout: 2 * time.Second, Reset: true,
	})
	fmt.Println(first == second)
	// Output: false
}

// Example_scenarios_newClientUncachedIndependent 演示 NewClientUncached 每次创建独立客户端，
// 适合租户隔离和包含闭包的动态配置。
func Example_scenarios_newClientUncachedIndependent() {
	first, _ := NewClientUncached(tn.ClientParam{})
	second, _ := NewClientUncached(tn.ClientParam{})
	fmt.Println(first == second)
	first.CloseIdleConnections()
	second.CloseIdleConnections()
	// Output: false
}

// Example_scenarios_newClientRejectsInvalidConfig 演示 NewClient 在创建阶段返回配置错误。
func Example_scenarios_newClientRejectsInvalidConfig() {
	_, err := NewClient(tn.ClientParam{Timeout: -time.Second})
	fmt.Println(errors.Is(err, tn.ErrClientConfig))
	// Output: true
}

// =====================================================================
// 13. 日志脱敏
// =====================================================================

// Example_scenarios_echoReqLogsMetadata 演示 EchoReq=true 输出请求摘要日志，
// 包含方法、origin、request_id 等元数据，但不含 URL 路径/查询参数。
func Example_scenarios_echoReqLogsMetadata() {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL + "/secret?token=hidden", Method: "GET",
		EchoReq: true, Logger: logger,
	}
	_, _, _ = Api(nil, p)
	fmt.Println(strings.Contains(output.String(), "http.request"))
	// Output: true
}

// Example_scenarios_logBodyRedactsSensitiveFields 演示 LogBody=true 时对 JSON 正文做脱敏，
// 内置 password/token 等字段被替换为 [REDACTED]。
func Example_scenarios_logBodyRedactsSensitiveFields() {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer server.Close()

	payload, _ := json.Marshal(map[string]any{"username": "alice", "password": "secret-value"})
	reader, ct, _ := tc.ContentByJson(payload)
	p := tn.ApiParam{
		Url: server.URL, Method: "POST", Body: reader,
		Header:  map[string]string{"Content-Type": ct},
		EchoReq: true, LogBody: true, Logger: logger,
	}
	_, _, _ = Api(nil, p)
	fmt.Println(strings.Contains(output.String(), "[REDACTED]"))
	// Output: true
}

// Example_scenarios_logBodyCustomSensitiveFields 演示 SensitiveFields 追加业务自定义字段，
// 与内置规则一起参与脱敏。
func Example_scenarios_logBodyCustomSensitiveFields() {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer server.Close()

	payload, _ := json.Marshal(map[string]any{"mobile": "13800000000"})
	reader, ct, _ := tc.ContentByJson(payload)
	p := tn.ApiParam{
		Url: server.URL, Method: "POST", Body: reader,
		Header:  map[string]string{"Content-Type": ct},
		EchoReq: true, LogBody: true, Logger: logger,
		SensitiveFields: []string{"mobile"},
	}
	_, _, _ = Api(nil, p)
	fmt.Println(strings.Contains(output.String(), "13800000000"))
	// Output: false
}

// Example_scenarios_traceIdAndCallerInLog 演示 TraceId 与 Caller 写入日志对应字段，
// 便于跨服务链路追踪。
func Example_scenarios_traceIdAndCallerInLog() {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "GET",
		EchoRes: true, Logger: logger,
		TraceId: "trace-xyz", Caller: "order.Create",
	}
	_, _, _ = Api(nil, p)
	log := output.String()
	fmt.Println(strings.Contains(log, "trace-xyz") && strings.Contains(log, "order.Create"))
	// Output: true
}

// =====================================================================
// 14. 压缩响应
// =====================================================================

// Example_scenarios_gzipResponseTransparentlyDecoded 演示 Content-Encoding: gzip 的响应
// 被自动解压，调用方拿到的是解压后的正文。
func Example_scenarios_gzipResponseTransparentlyDecoded() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = io.WriteString(writer, "hello-gzip")
		_ = writer.Close()
	}))
	defer server.Close()

	body, _, err := Api(nil, tn.ApiParam{Url: server.URL, Method: "GET"})
	fmt.Printf("body=%q err=%v\n", string(body), err)
	// Output: body="hello-gzip" err=<nil>
}

// Example_scenarios_gzipLimitAppliesToDecoded 演示 MaxResponseBytes 对解压后的正文计数，
// 超限返回 ErrResponseTooLarge。
func Example_scenarios_gzipLimitAppliesToDecoded() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = io.WriteString(writer, strings.Repeat("x", 8192))
		_ = writer.Close()
	}))
	defer server.Close()

	p := tn.ApiParam{Url: server.URL, Method: "GET", MaxResponseBytes: 64}
	body, _, err := Api(nil, p)
	fmt.Printf("err=%v n=%d\n", errors.Is(err, tn.ErrResponseTooLarge), len(body))
	// Output: err=true n=64
}

// =====================================================================
// 15. HTTP/2 与 TLS
// =====================================================================

// Example_scenarios_http2Negotiated 演示 httptest TLS 服务器可协商 HTTP/2，
// 且默认客户端能正确处理。
func Example_scenarios_http2Negotiated() {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.ProtoMajor)
		_, _ = io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	_, _, _ = Api(server.Client(), tn.ApiParam{Url: server.URL, Method: "GET"})
	// Output: 2
}

// Example_scenarios_untrustedTLSRejected 演示默认客户端拒绝不受信任的 TLS 证书。
func Example_scenarios_untrustedTLSRejected() {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	_, _, err := Api(nil, tn.ApiParam{Url: server.URL, Method: "GET"})
	fmt.Println(err != nil)
	// Output: true
}

// =====================================================================
// 16. 中间件
// =====================================================================

// Example_scenarios_customMiddlewareForPlainClient 演示自定义中间件
// 可用于普通 http.Client，不影响其基本请求能力。
func Example_scenarios_customMiddlewareForPlainClient() {
	client, _ := NewClient(tn.ClientParam{
		SpaceName: "middleware-example",
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(200, "ok"), nil
		}),
		Middlewares: []interfaces.Middleware{&clonedMiddleware{}},
	})
	resp, err := client.Get("http://example.test")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	fmt.Println(string(data))
	// Output: ok
}

// Example_scenarios_middlewareFactoryIsolation 演示 MiddlewareFactories 构造的独立节点
// 不会因复用而相互污染。
func Example_scenarios_middlewareFactoryIsolation() {
	prototype := &clonedMiddleware{}
	_, err := NewClientUncached(tn.ClientParam{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(200, "ok"), nil
		}),
		Middlewares: []interfaces.Middleware{prototype},
	})
	fmt.Println(err, prototype.next == nil)
	// Output: <nil> true
}

// =====================================================================
// 17. 参数校验
// =====================================================================

// Example_scenarios_invalidParametersNeverSend 演示无效参数在准备阶段被拒绝，
// 不会建立连接或发送请求。
func Example_scenarios_invalidParametersNeverSend() {
	for _, mutate := range []func(*tn.ApiParam){
		func(p *tn.ApiParam) { p.Retry = -1 },
		func(p *tn.ApiParam) { p.SingleTimeout = -1 },
		func(p *tn.ApiParam) { p.Url = "http://user:secret@example.test" },
		func(p *tn.ApiParam) { p.Method = "WAT" },
		func(p *tn.ApiParam) { p.Header = map[string]string{"X-Test": "x\r\nInjected:yes"} },
	} {
		p := tn.ApiParam{Url: "http://example.test", Method: "GET"}
		mutate(&p)
		_, _, err := Api(nil, p)
		fmt.Println(err != nil)
	}
	// Output:
	// true
	// true
	// true
	// true
	// true
}

// Example_scenarios_declaredZeroLengthEmptyBodyAccepted 演示显式 Content-Length: 0
// 与空正文一致时被接受。
func Example_scenarios_declaredZeroLengthEmptyBodyAccepted() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		fmt.Printf("n=%d\n", len(data))
	}))
	defer server.Close()

	p := tn.ApiParam{
		Url: server.URL, Method: "POST",
		Body:   strings.NewReader(""),
		Header: map[string]string{"Content-Length": "0"},
	}
	_, _, _ = Api(nil, p)
	// Output: n=0
}

// Example_scenarios_transferEncodingHeaderRejected 演示 Transfer-Encoding 头由 net/http 管理，
// 不能通过 Header 字段覆盖。
func Example_scenarios_transferEncodingHeaderRejected() {
	_, _, err := Api(nil, tn.ApiParam{
		Url:    "http://example.test",
		Method: "GET",
		Header: map[string]string{"Transfer-Encoding": "chunked"},
	})
	fmt.Println(err != nil)
	// Output: true
}
