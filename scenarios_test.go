// Package requests 场景测试集合。
//
// 本文件按场景维度系统性地覆盖 requests 库的全部能力，每个用例都附带中文注释，
// 说明它验证的目标和涉及的字段。用例之间相互独立，可单独运行：
//
//	go test -run TestScenarios ./...
//
// 注释按以下分类组织：
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
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhlanshan/requests/interfaces"
	"github.com/dhlanshan/requests/logging"
	"github.com/dhlanshan/requests/tc"
	"github.com/dhlanshan/requests/tn"
)

// =====================================================================
// 1. HTTP 方法与基础请求
// =====================================================================

// TestScenarios_AllHttpMethods 遍历库声明支持的全部 HTTP 方法，
// 验证方法名原样写入请求行，不会被自动转换大小写。
func TestScenarios_AllHttpMethods(t *testing.T) {
	for _, method := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodConnect,
	} {
		t.Run(method, func(t *testing.T) {
			p := params()
			p.Method = method
			client := testClient(func(req *http.Request) (*http.Response, error) {
				if req.Method != method {
					t.Fatalf("method lost: got %q want %q", req.Method, method)
				}
				return response(200, "ok"), nil
			})
			if _, _, err := Api(client, p); err != nil {
				t.Fatalf("method %s: %v", method, err)
			}
		})
	}
}

// TestScenarios_SuccessStatuses 验证默认 2xx 全部判定为成功，
// 并能完整返回 Body 与 Header；204 这类无正文响应也按成功处理。
func TestScenarios_SuccessStatuses(t *testing.T) {
	for _, status := range []int{200, 201, 202, 203, 204, 205, 206, 226} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p := params()
			body, header, err := Api(testClient(func(*http.Request) (*http.Response, error) {
				return response(status, "ok"), nil
			}), p)
			if err != nil || string(body) != "ok" || header.Get("Content-Type") == "" {
				t.Fatalf("status=%d body=%q err=%v", status, body, err)
			}
		})
	}
}

// TestScenarios_DefaultClientNil 验证 client=nil 时使用进程内默认客户端，
// 不会因为传入 nil panic；默认客户端具备连接池和默认超时。
func TestScenarios_DefaultClientNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	p := params()
	p.Url = server.URL
	body, _, err := Api(nil, p)
	if err != nil || string(body) != "ok" {
		t.Fatalf("default client failed: %v", err)
	}
}

// TestScenarios_CustomSuccessStatus 验证 SuccessStatus 回调完全替换默认 2xx 判定，
// 例如同时接受 2xx 与 304。
func TestScenarios_CustomSuccessStatus(t *testing.T) {
	p := params()
	p.SuccessStatus = func(code int) bool { return code == 200 || code == 304 }
	for _, status := range []int{200, 304} {
		calls := 0
		s := status
		_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
			calls++
			return response(s, ""), nil
		}), p)
		if err != nil || calls != 1 {
			t.Fatalf("status=%d err=%v calls=%d", s, err, calls)
		}
	}
	// 400 不在白名单内，应返回 HTTPError。
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		return response(400, "bad"), nil
	}), p)
	var httpErr *tn.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 400 {
		t.Fatalf("err=%v", err)
	}
}

// =====================================================================
// 2. Body 构造方式（tc 包）
// =====================================================================

// TestScenarios_BodyJSON 验证 tc.ContentByJson 返回的 Body 与 Content-Type 能被对端读取。
func TestScenarios_BodyJSON(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"k": "v"})
	reader, ct, err := tc.ContentByJson(payload)
	if err != nil || ct != "application/json" {
		t.Fatal(ct, err)
	}
	p := params()
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Type": ct}
	_, _, err = Api(testClient(func(req *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(req.Body)
		if string(data) != string(payload) || req.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("json body=%q ct=%s", data, req.Header.Get("Content-Type"))
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
}

// TestScenarios_BodyXML 验证 tc.ContentByXml 的 Content-Type 与正文透传。
func TestScenarios_BodyXML(t *testing.T) {
	payload := []byte("<root><k>v</k></root>")
	reader, ct, err := tc.ContentByXml(payload)
	if err != nil || ct != "application/xml" {
		t.Fatal(ct, err)
	}
	p := params()
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Type": ct}
	_, _, err = Api(testClient(func(req *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(req.Body)
		if string(data) != string(payload) {
			t.Fatalf("xml body=%q", data)
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
}

// TestScenarios_BodyRaw 验证 tc.ContentByRaw 的纯文本正文。
func TestScenarios_BodyRaw(t *testing.T) {
	reader, ct, err := tc.ContentByRaw("plain text body")
	if err != nil || ct != "text/plain" {
		t.Fatal(ct, err)
	}
	p := params()
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Type": ct}
	_, _, err = Api(testClient(func(req *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(req.Body)
		if string(data) != "plain text body" {
			t.Fatalf("raw body=%q", data)
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
}

// TestScenarios_BodyBinary 验证 tc.ContentByBinary 字节切片的长度与 Content-Type。
func TestScenarios_BodyBinary(t *testing.T) {
	payload := []byte{0x00, 0x01, 0x02, 0xFF}
	reader, n, ct, err := tc.ContentByBinary(payload)
	if err != nil || ct != "application/octet-stream" || n != int64(len(payload)) {
		t.Fatal(ct, n, err)
	}
	p := params()
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Type": ct}
	_, _, err = Api(testClient(func(req *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(req.Body)
		if !bytes.Equal(data, payload) {
			t.Fatalf("binary body=%v", data)
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
}

// TestScenarios_BodyMsgpack 验证 tc.ContentByMsgpack 编码后正文非空且 Content-Type 正确。
func TestScenarios_BodyMsgpack(t *testing.T) {
	reader, ct, err := tc.ContentByMsgpack(map[string]any{"k": "v"})
	if err != nil || ct != "application/x-msgpack" {
		t.Fatal(ct, err)
	}
	p := params()
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Type": ct}
	_, _, err = Api(testClient(func(req *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(req.Body)
		if len(data) == 0 || req.Header.Get("Content-Type") != "application/x-msgpack" {
			t.Fatalf("msgpack body=%v ct=%s", data, req.Header.Get("Content-Type"))
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
}

// TestScenarios_BodyFormUrlencoded 验证 tc.ContentByXWFormUrlencoded 的字段编码。
func TestScenarios_BodyFormUrlencoded(t *testing.T) {
	reader, ct, err := tc.ContentByXWFormUrlencoded(map[string]any{"a": "1", "b": "two"})
	if err != nil || ct != "application/x-www-form-urlencoded" {
		t.Fatal(ct, err)
	}
	p := params()
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Type": ct}
	_, _, err = Api(testClient(func(req *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(req.Body)
		parsed, _ := url.ParseQuery(string(data))
		if parsed.Get("a") != "1" || parsed.Get("b") != "two" {
			t.Fatalf("form body=%q", data)
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
}

// TestScenarios_BodyMultipartBytes 验证 tc.ContentByFormData 上传内存字节切片文件，
// 对端可解析出文件内容与普通字段。
func TestScenarios_BodyMultipartBytes(t *testing.T) {
	reader, ct, err := tc.ContentByFormData(map[string]any{
		"file": tc.FormFile{Filename: "f.bin", Content: []byte("hello")},
		"desc": "desc-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	p := params()
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Type": ct}
	_, _, err = Api(testClient(func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(1 << 16); err != nil {
			t.Fatal(err)
		}
		file, _, _ := req.FormFile("file")
		data, _ := io.ReadAll(file)
		file.Close()
		if string(data) != "hello" || req.FormValue("desc") != "desc-value" {
			t.Fatalf("multipart file=%q desc=%q", data, req.FormValue("desc"))
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
}

// TestScenarios_BodyFactoryReopensForRetry 验证 BodyFactory 在重试时重新打开独立流，
// 每次返回的 ReadCloser 各自维护读取位置，互不影响。
func TestScenarios_BodyFactoryReopensForRetry(t *testing.T) {
	var opened atomic.Int32
	p := params()
	p.Method, p.Retry, p.AllowUnsafeRetry = "POST", 1, true
	p.BodyFactory = func(context.Context) (io.ReadCloser, int64, error) {
		opened.Add(1)
		return io.NopCloser(strings.NewReader("payload")), 7, nil
	}
	calls := 0
	_, _, err := Api(testClient(func(req *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(req.Body)
		if string(data) != "payload" {
			t.Fatalf("body=%q", data)
		}
		calls++
		if calls == 1 {
			return response(503, ""), nil
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil || calls != 2 || opened.Load() != 2 {
		t.Fatalf("calls=%d opened=%d err=%v", calls, opened.Load(), err)
	}
}

// TestScenarios_OneShotBodyTooLarge 验证真正的一次性流（无 GetBody）在重试时
// 超过 MaxReplayBytes 会被拒绝，避免无限内存占用。
func TestScenarios_OneShotBodyTooLarge(t *testing.T) {
	p := params()
	p.Method, p.Retry, p.AllowUnsafeRetry = "POST", 1, true
	p.Body = &failingReader{data: "123456"} // 一次性流，无 GetBody
	p.MaxReplayBytes = 4
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("oversized one-shot body should not be sent")
		return nil, nil
	}), p)
	if !errors.Is(err, tn.ErrRequestBodyTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

// TestScenarios_BodyAndBodyFactoryMutex 验证 Body 与 BodyFactory 不能同时设置。
func TestScenarios_BodyAndBodyFactoryMutex(t *testing.T) {
	p := params()
	p.Body = strings.NewReader("x")
	p.BodyFactory = func(context.Context) (io.ReadCloser, int64, error) {
		return io.NopCloser(strings.NewReader("y")), 1, nil
	}
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("mutually exclusive body fields accepted")
		return nil, nil
	}), p)
	if err == nil {
		t.Fatal("expected error for Body+BodyFactory")
	}
}

// =====================================================================
// 3. URL 参数与请求头
// =====================================================================

// TestScenarios_QueryParamsAppend 验证 Params 在 Url 已有查询参数之后追加，
// 同名键会形成多值，原始值不被覆盖。
func TestScenarios_QueryParamsAppend(t *testing.T) {
	p := params()
	p.Url += "?tag=a"
	p.Params = map[string]string{"tag": "b", "page": "1"}
	_, _, err := Api(testClient(func(req *http.Request) (*http.Response, error) {
		tags := req.URL.Query()["tag"]
		if len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
			t.Fatalf("tags=%v", tags)
		}
		if req.URL.Query().Get("page") != "1" {
			t.Fatalf("page=%v", req.URL.Query().Get("page"))
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
}

// TestScenarios_HostHeader 验证 Host 头写入 Request.Host，而非普通 Header。
func TestScenarios_HostHeader(t *testing.T) {
	p := params()
	p.Header = map[string]string{"Host": "virtual.example", "X-Id": "abc"}
	_, _, err := Api(testClient(func(req *http.Request) (*http.Response, error) {
		if req.Host != "virtual.example" {
			t.Fatalf("host=%q", req.Host)
		}
		if req.Header.Get("X-Id") != "abc" {
			t.Fatalf("X-Id=%q", req.Header.Get("X-Id"))
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
}

// TestScenarios_CookieJar 验证 Jar 配置后跨请求自动携带 Cookie。
func TestScenarios_CookieJar(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "token", Value: "abc", Path: "/"})
			return
		}
		cookie, cookieErr := r.Cookie("token")
		if cookieErr != nil || cookie.Value != "abc" {
			t.Error("cookie not carried")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client, err := NewClient(tn.ClientParam{SpaceName: t.Name(), Jar: jar})
	if err != nil {
		t.Fatal(err)
	}
	p := params()
	p.Url = server.URL + "/set"
	if _, _, err := Api(client, p); err != nil {
		t.Fatal(err)
	}
	p.Url = server.URL + "/get"
	if body, _, err := Api(client, p); err != nil || string(body) != "ok" {
		t.Fatal(body, err)
	}
}

// =====================================================================
// 4. 超时与取消
// =====================================================================

// TestScenarios_SingleTimeout 验证 SingleTimeout 限制单次尝试，
// 超时后表现为 context.DeadlineExceeded；总 Context 仍允许下一次重试。
func TestScenarios_SingleTimeout(t *testing.T) {
	p := params()
	p.Retry, p.SingleTimeout, p.Timeout = 1, 15*time.Millisecond, time.Second
	calls := 0
	_, _, err := Api(testClient(func(req *http.Request) (*http.Response, error) {
		calls++
		<-req.Context().Done()
		return nil, req.Context().Err()
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_TotalTimeout 验证 Timeout 覆盖整次调用（含所有重试等待），
// 一旦到期立即停止后续尝试。
func TestScenarios_TotalTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	p := params()
	p.Retry = 10
	p.RetryInterval = 50 * time.Millisecond // 单次等待已超过总期限
	calls := 0
	_, _, err := ApiContext(ctx, testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(503, ""), nil
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if calls < 1 {
		t.Fatalf("no attempt made: %d", calls)
	}
}

// TestScenarios_ParentCancel 验证上游 Context 取消时立即返回 context.Canceled，
// 不会发出请求。
func TestScenarios_ParentCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := ApiContext(ctx, testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("sent after parent cancel")
		return nil, nil
	}), params())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

// TestScenarios_CancelDuringBodyPrepare 验证 Body 准备期间取消会立即返回，
// 不会等待 Body 读取完成。
func TestScenarios_CancelDuringBodyPrepare(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	p := params()
	p.Body, p.Retry = reader, 1
	_, _, err := ApiContext(ctx, testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("sent unreadable body")
		return nil, nil
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

// TestScenarios_CancelDuringRetryWait 验证重试等待期间取消会立即返回，
// 不会等待 Retry-After 指定的长时间。
func TestScenarios_CancelDuringRetryWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	p := params()
	p.Retry = 10
	calls := 0
	_, _, err := ApiContext(ctx, testClient(func(*http.Request) (*http.Response, error) {
		calls++
		resp := response(429, "")
		resp.Header.Set("Retry-After", "999999999999")
		return resp, nil
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_CancelDuringResponseRead 验证响应读取期间 SingleTimeout 到期
// 会被识别为单次失败，错误为 context.DeadlineExceeded。
func TestScenarios_CancelDuringResponseRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	p := params()
	p.SingleTimeout = 20 * time.Millisecond
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		resp := response(200, "")
		resp.Body = reader
		return resp, nil
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

// =====================================================================
// 5. 重试策略
// =====================================================================

// TestScenarios_GetRetriesByDefault 验证 GET 默认允许重试，
// 命中默认可重试状态码（503）时会按 Retry 次数重试。
func TestScenarios_GetRetriesByDefault(t *testing.T) {
	p := params()
	p.Retry = 2
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(503, ""), nil
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_PostNotRetriedByDefault 验证 POST 默认不重试，
// 即使状态码可重试也只执行一次。
func TestScenarios_PostNotRetriedByDefault(t *testing.T) {
	p := params()
	p.Method, p.Retry, p.Body = "POST", 2, strings.NewReader("x")
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(503, ""), nil
	}), p)
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_PostOptInUnsafeRetry 验证 POST 配置 AllowUnsafeRetry=true 后
// 允许重试，调用方显式接受重复操作的业务风险。
func TestScenarios_PostOptInUnsafeRetry(t *testing.T) {
	p := params()
	p.Method, p.Retry, p.AllowUnsafeRetry = "POST", 2, true
	p.Body = strings.NewReader("x")
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(503, ""), nil
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_IdempotencyKeyEnablesRetry 验证非空 Idempotency-Key 请求头
// 可以放行 POST 等非安全方法的重试（依赖服务端去重）。
func TestScenarios_IdempotencyKeyEnablesRetry(t *testing.T) {
	p := params()
	p.Method, p.Retry = "POST", 2
	p.Body = strings.NewReader("x")
	p.Header = map[string]string{"Idempotency-Key": "operation-1"}
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(503, ""), nil
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_BlankIdempotencyKeyDoesNotEnableRetry 验证空白 Idempotency-Key
// 不会放行非安全方法的重试。
func TestScenarios_BlankIdempotencyKeyDoesNotEnableRetry(t *testing.T) {
	p := params()
	p.Method, p.Retry = "POST", 2
	p.Header = map[string]string{"Idempotency-Key": " "}
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(503, ""), nil
	}), p)
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_DefaultRetryStatusCodes 验证默认可重试状态码集合
// (408, 429, 500, 502, 503, 504) 均会触发重试。
func TestScenarios_DefaultRetryStatusCodes(t *testing.T) {
	for _, status := range []int{408, 429, 500, 502, 503, 504} {
		s := status
		t.Run(http.StatusText(s), func(t *testing.T) {
			p := params()
			p.Retry = 1
			calls := 0
			_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return response(s, ""), nil
				}
				return response(200, "ok"), nil
			}), p)
			if err != nil || calls != 2 {
				t.Fatalf("status=%d calls=%d err=%v", s, calls, err)
			}
		})
	}
}

// TestScenarios_CustomRetryStatusCodes 验证 RetryStatusCodes 非 nil 时完全替换默认集合，
// 例如只重试 500。
func TestScenarios_CustomRetryStatusCodes(t *testing.T) {
	p := params()
	p.Retry = 1
	p.RetryStatusCodes = []int{500}
	// 500 在自定义集合内，应重试。
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(500, ""), nil
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil || calls != 2 {
		t.Fatalf("500 should retry: calls=%d err=%v", calls, err)
	}
	// 503 不在自定义集合内，不应重试。
	calls = 0
	_, _, err = Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(503, ""), nil
	}), p)
	if err == nil || calls != 1 {
		t.Fatalf("503 should not retry: calls=%d err=%v", calls, err)
	}
}

// TestScenarios_EmptyRetryStatusCodesDisables 验证非 nil 空切片关闭状态码重试，
// 与 nil（使用默认）严格区分。
func TestScenarios_EmptyRetryStatusCodesDisables(t *testing.T) {
	p := params()
	p.Retry = 1
	p.RetryStatusCodes = []int{}
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(503, ""), nil
	}), p)
	if err == nil || calls != 1 {
		t.Fatalf("empty codes should disable retry: calls=%d err=%v", calls, err)
	}
}

// TestScenarios_NetworkErrorRetried 验证网络错误（如连接重置）会触发重试，
// 即使状态码不在 RetryStatusCodes 内。
func TestScenarios_NetworkErrorRetried(t *testing.T) {
	p := params()
	p.Retry = 2
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_PermanentErrorNotRetried 验证 tn.NoRetry 标记的永久错误
// 不会被重试，即使方法允许重试。
func TestScenarios_PermanentErrorNotRetried(t *testing.T) {
	cause := errors.New("permanent")
	p := params()
	p.Retry = 2
	p.RetryDecider = func(context.Context, tn.RetryInfo) bool { return true } // 仍无法绕过 NoRetry
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, tn.NoRetry(cause)
	}), p)
	if !errors.Is(err, cause) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_RetryDeciderOverride 验证 RetryDecider 可覆盖默认重试判断，
// 但无法绕过方法安全检查、NoRetry 等硬性限制。
func TestScenarios_RetryDeciderOverride(t *testing.T) {
	p := params()
	p.Retry = 2
	decisions := 0
	p.RetryDecider = func(_ context.Context, info tn.RetryInfo) bool {
		decisions++
		if info.Attempt < 1 {
			t.Error("invalid attempt number")
		}
		return true
	}
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(400, "bad request"), nil // 400 默认不可重试
	}), p)
	if err == nil || calls != 3 || decisions != 2 {
		t.Fatalf("calls=%d decisions=%d err=%v", calls, decisions, err)
	}
}

// TestScenarios_RetryAfterHeaderHonored 验证服务端 Retry-After 头（秒数）
// 会延长重试等待，但仍受 MaxRetryDelay 限制。
func TestScenarios_RetryAfterHeaderHonored(t *testing.T) {
	p := params()
	p.Retry = 1
	p.MaxRetryDelay = 50 * time.Millisecond // 截断 Retry-After
	p.RetryInterval = time.Nanosecond
	start := time.Now()
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			resp := response(503, "")
			resp.Header.Set("Retry-After", "9999")
			return resp, nil
		}
		return response(200, "ok"), nil
	}), p)
	elapsed := time.Since(start)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	// 实际等待被 MaxRetryDelay 截断为 50ms，加上执行开销。
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Retry-After not capped: %v", elapsed)
	}
}

// TestScenarios_BackoffFunction 验证自定义 Backoff 函数优先于 RetryInterval 和默认指数退避。
func TestScenarios_BackoffFunction(t *testing.T) {
	p := params()
	p.Retry = 2
	p.Backoff = func(retry int) time.Duration { return time.Nanosecond }
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return response(503, ""), nil
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_RetryIntervalFixed 验证 RetryInterval 提供固定重试间隔。
func TestScenarios_RetryIntervalFixed(t *testing.T) {
	p := params()
	p.Retry = 1
	p.RetryInterval = time.Nanosecond
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(503, ""), nil
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// =====================================================================
// 6. 业务校验
// =====================================================================

// TestScenarios_DefaultValidator 验证开启 EnableValid 但不提供验证器时，
// 默认要求 JSON 中 code == 200 且 msg == "success"。
func TestScenarios_DefaultValidator(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want error
	}{
		{"success", `{"code":200,"msg":"success"}`, nil},
		{"wrong_code", `{"code":500,"msg":"success"}`, tn.ErrValidation},
		{"wrong_msg", `{"code":200,"msg":"fail"}`, tn.ErrValidation},
		{"invalid_json", `not-json`, tn.ErrValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := params()
			p.EnableValid = true
			_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
				return response(200, tc.body), nil
			}), p)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("err=%v", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

// TestScenarios_CustomValidator 验证自定义 Validator 的返回值含义：
// 仅 (true, nil) 表示通过；其他组合（含 panic）都视为校验失败。
func TestScenarios_CustomValidator(t *testing.T) {
	cause := errors.New("business error")
	for _, tc := range []struct {
		name string
		fn   tn.Validator
		want error
	}{
		{"false_nil", func(context.Context, []byte, http.Header) (bool, error) { return false, nil }, tn.ErrValidation},
		{"true_error", func(context.Context, []byte, http.Header) (bool, error) { return true, cause }, cause},
		{"panic", func(context.Context, []byte, http.Header) (bool, error) { panic("private data") }, tn.ErrValidation},
		{"pass", func(context.Context, []byte, http.Header) (bool, error) { return true, nil }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := params()
			p.EnableValid, p.Validator = true, tc.fn
			_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
				return response(200, `{"code":200,"msg":"success"}`), nil
			}), p)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// TestScenarios_ValidatorCancellation 验证 Validator 可接收取消信号，
// 超时后即使返回 (true, nil) 也按超时处理。
func TestScenarios_ValidatorCancellation(t *testing.T) {
	p := params()
	p.SingleTimeout, p.EnableValid = 10*time.Millisecond, true
	p.Validator = func(ctx context.Context, _ []byte, _ http.Header) (bool, error) {
		<-ctx.Done()
		return true, nil
	}
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		return response(200, ""), nil
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late validation succeeded: %v", err)
	}
}

// TestScenarios_ValidationFailureNoRetryByDefault 验证校验失败默认不重试，
// 避免对永久业务失败重复请求。
func TestScenarios_ValidationFailureNoRetryByDefault(t *testing.T) {
	p := params()
	p.Retry, p.EnableValid = 2, true
	p.Validator = func(context.Context, []byte, http.Header) (bool, error) { return false, nil }
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, "business"), nil
	}), p)
	if !errors.Is(err, tn.ErrValidation) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_ValidationRetryEnabled 验证 RetryValidation=true 允许校验失败重试，
// 适合临时业务状态。
func TestScenarios_ValidationRetryEnabled(t *testing.T) {
	p := params()
	p.Retry, p.EnableValid, p.RetryValidation = 2, true, true
	p.Validator = func(context.Context, []byte, http.Header) (bool, error) { return false, nil }
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, "business"), nil
	}), p)
	if !errors.Is(err, tn.ErrValidation) || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_ValidationNoRetryMark 验证 tn.NoRetry 标记的校验错误
// 即使 RetryValidation=true 也不会重试。
func TestScenarios_ValidationNoRetryMark(t *testing.T) {
	permanent := errors.New("permanent business failure")
	p := params()
	p.Retry, p.EnableValid, p.RetryValidation = 2, true, true
	p.Validator = func(context.Context, []byte, http.Header) (bool, error) { return false, tn.NoRetry(permanent) }
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, "business"), nil
	}), p)
	if !errors.Is(err, permanent) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// =====================================================================
// 7. 重定向安全
// =====================================================================

// TestScenarios_SameOriginRedirect 验证同源重定向默认允许，
// 请求头与正文都会被保留并重发。
func TestScenarios_SameOriginRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusTemporaryRedirect)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "payload" || r.Header.Get("X-Test") != "yes" {
			t.Errorf("redirect lost data: body=%q header=%q", body, r.Header.Get("X-Test"))
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	p := params()
	p.Url, p.Method, p.Body = server.URL+"/start", "POST", strings.NewReader("payload")
	p.Header = map[string]string{"X-Test": "yes"}
	body, _, err := Api(server.Client(), p)
	if err != nil || string(body) != "ok" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

// TestScenarios_CrossOriginRedirectDeniedByDefault 验证跨源重定向默认被拒绝，
// 不会发出对端请求。
func TestScenarios_CrossOriginRedirectDeniedByDefault(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("cross-origin target was reached")
		_, _ = io.WriteString(w, "leaked")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	p := params()
	p.Url = source.URL
	_, _, err := Api(source.Client(), p)
	if !errors.Is(err, tn.ErrTargetBlocked) {
		t.Fatalf("err=%v", err)
	}
}

// TestScenarios_CrossOriginRedirectStripsSecrets 验证显式允许跨源跳转后，
// 仅保留白名单内的请求头，Authorization/Cookie 等凭证被清理。
func TestScenarios_CrossOriginRedirectStripsSecrets(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, key := range []string{"Authorization", "Cookie", "X-Api-Key"} {
			if r.Header.Get(key) != "" {
				t.Errorf("%s leaked", key)
			}
		}
		if r.Header.Get("Accept") == "" {
			t.Error("default allowlist header lost")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	p := params()
	p.Url = source.URL
	p.AllowCrossOriginRedirect = true
	p.Header = map[string]string{"Authorization": "secret", "Cookie": "a=b", "X-Api-Key": "key", "Accept": "application/json"}
	_, _, err := Api(source.Client(), p)
	if err != nil {
		t.Fatal(err)
	}
}

// TestScenarios_HTTPSDowngradeDenied 验证 HTTPS→HTTP 降级默认被拒绝，
// 即使已允许跨源跳转。
func TestScenarios_HTTPSDowngradeDenied(t *testing.T) {
	calls := 0
	client := testClient(func(req *http.Request) (*http.Response, error) {
		calls++
		res := response(302, "")
		switch req.URL.Path {
		case "/start":
			res.Header.Set("Location", "https://service.test/secure")
		case "/secure":
			res.Header.Set("Location", "http://service.test/final")
		}
		return res, nil
	})
	p := params()
	p.Url = "https://service.test/start"
	p.AllowCrossOriginRedirect = true
	_, _, err := Api(client, p)
	if !errors.Is(err, tn.ErrTargetBlocked) || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_HTTPSDowngradeAllowed 验证 AllowHTTPSDowngrade=true 显式放行降级。
func TestScenarios_HTTPSDowngradeAllowed(t *testing.T) {
	calls := 0
	client := testClient(func(req *http.Request) (*http.Response, error) {
		calls++
		res := response(200, "ok")
		switch req.URL.Path {
		case "/start":
			res.StatusCode = 302
			res.Header.Set("Location", "https://service.test/secure")
		case "/secure":
			res.StatusCode = 302
			res.Header.Set("Location", "http://service.test/final")
		}
		return res, nil
	})
	p := params()
	p.Url = "https://service.test/start"
	p.AllowCrossOriginRedirect, p.AllowHTTPSDowngrade = true, true
	_, _, err := Api(client, p)
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_CrossOriginBodyReplayDenied 验证跨源 307/308 重放正文默认被拒绝，
// 必须显式 AllowCrossOriginBody。
func TestScenarios_CrossOriginBodyReplayDenied(t *testing.T) {
	calls := 0
	client := testClient(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			res := response(307, "")
			res.Header.Set("Location", "https://other.test/next")
			return res, nil
		}
		t.Error("cross-origin body replayed without permission")
		return response(200, "ok"), nil
	})
	p := params()
	p.Url, p.Method, p.Body = "https://source.test/start", "POST", strings.NewReader("private-body")
	p.AllowCrossOriginRedirect = true
	_, _, err := Api(client, p)
	if !errors.Is(err, tn.ErrTargetBlocked) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_CrossOriginBodyReplayAllowed 验证 AllowCrossOriginBody=true
// 放行跨源 307/308 正文重放。
func TestScenarios_CrossOriginBodyReplayAllowed(t *testing.T) {
	calls := 0
	client := testClient(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			res := response(307, "")
			res.Header.Set("Location", "https://other.test/next")
			return res, nil
		}
		data, _ := io.ReadAll(req.Body)
		if string(data) != "private-body" {
			t.Errorf("body=%q", data)
		}
		return response(200, "ok"), nil
	})
	p := params()
	p.Url, p.Method, p.Body = "https://source.test/start", "POST", strings.NewReader("private-body")
	p.AllowCrossOriginRedirect, p.AllowCrossOriginBody = true, true
	_, _, err := Api(client, p)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_MaxRedirectsExceeded 验证 MaxRedirects 限制跳转次数，
// 超出后返回 ErrTargetBlocked。
func TestScenarios_MaxRedirectsExceeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound) // 无限自跳
	}))
	defer server.Close()
	p := params()
	p.Url = server.URL
	p.MaxRedirects = 2
	_, _, err := Api(server.Client(), p)
	if !errors.Is(err, tn.ErrTargetBlocked) {
		t.Fatalf("err=%v", err)
	}
}

// TestScenarios_CustomCheckRedirect 验证 ClientParam.CheckRedirect 回调先于库默认检查执行，
// 可用于更早停止或记录跳转。
func TestScenarios_CustomCheckRedirect(t *testing.T) {
	var redirects atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	client, err := NewClient(tn.ClientParam{
		SpaceName: t.Name(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			redirects.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := params()
	p.Url = server.URL + "/start"
	if _, _, err := Api(client, p); err != nil {
		t.Fatal(err)
	}
	if redirects.Load() != 1 {
		t.Fatalf("redirects=%d", redirects.Load())
	}
}

// =====================================================================
// 8. 错误处理与诊断
// =====================================================================

// TestScenarios_HTTPErrorPreservesBody 验证 HTTP 状态失败时返回 *tn.HTTPError，
// 保留有界 Body 与 Header，便于处理业务错误响应。
func TestScenarios_HTTPErrorPreservesBody(t *testing.T) {
	p := params()
	body, header, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		return response(400, `{"code":400,"msg":"bad"}`), nil
	}), p)
	var httpErr *tn.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 400 ||
		string(body) != `{"code":400,"msg":"bad"}` || header.Get("Content-Type") == "" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

// TestScenarios_MaxErrorBytesTruncates 验证 MaxErrorBytes 限制错误正文保留长度，
// 超限时 HTTPError.Truncated=true 且支持 errors.Is(err, ErrResponseTooLarge)。
func TestScenarios_MaxErrorBytesTruncates(t *testing.T) {
	p := params()
	p.MaxErrorBytes = 4
	body, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		return response(400, "abcdef"), nil
	}), p)
	var httpErr *tn.HTTPError
	if !errors.As(err, &httpErr) || !errors.Is(err, tn.ErrResponseTooLarge) ||
		!httpErr.Truncated || string(body) != "abcd" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

// TestScenarios_MaxResponseBytesTruncates 验证 MaxResponseBytes 限制成功响应正文，
// 超限返回 ErrResponseTooLarge 并保留上限以内的部分数据。
func TestScenarios_MaxResponseBytesTruncates(t *testing.T) {
	p := params()
	p.MaxResponseBytes = 3
	body, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		return response(200, "abcdef"), nil
	}), p)
	if !errors.Is(err, tn.ErrResponseTooLarge) || string(body) != "abc" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

// TestScenarios_NetworkErrorOmitsURL 验证网络错误的展示文本不包含 URL 或查询参数中的敏感信息，
// 但通过 Unwrap 仍可获取原始错误用于诊断。
func TestScenarios_NetworkErrorOmitsURL(t *testing.T) {
	cause := errors.New("fixture network failure")
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		return nil, cause
	}), tn.ApiParam{Url: "https://example.test/private?token=fixture-secret", Method: "GET"})
	var requestErr *tn.RequestError
	var urlErr *url.Error
	if !errors.As(err, &requestErr) || !errors.As(err, &urlErr) ||
		!errors.Is(err, cause) || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unsafe or uninspectable error: %v", err)
	}
}

// TestScenarios_RequestErrorTimeoutTemporary 验证超时错误可被识别为 Timeout，
// 便于上层按超时分类处理。
func TestScenarios_RequestErrorTimeoutTemporary(t *testing.T) {
	p := params()
	p.SingleTimeout = 10 * time.Millisecond
	_, _, err := Api(testClient(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	// context.DeadlineExceeded 实现 net.Error，Timeout() 返回 true。
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("err=%v not recognized as timeout", err)
	}
}

// TestScenarios_InvalidQueryRejected 验证 URL 中非法的查询编码在准备阶段被拒绝，
// 不会发出请求。
func TestScenarios_InvalidQueryRejected(t *testing.T) {
	for _, query := range []string{"bad=%zz", "bad=%", "bad=x;y"} {
		p := params()
		p.Url += "?good=1&" + query
		_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid query sent")
			return nil, nil
		}), p)
		if err == nil {
			t.Fatal("invalid query accepted")
		}
	}
}

// =====================================================================
// 9. 安全策略（URLPolicy / NetworkPolicy）
// =====================================================================

// TestScenarios_URLPolicyBlocksInitialURL 验证 URLPolicy 在首次发送前检查合并 Params 后的 URL，
// 返回 ErrTargetBlocked 包装原始原因。
func TestScenarios_URLPolicyBlocksInitialURL(t *testing.T) {
	p := params()
	blocked := errors.New("forbidden path")
	p.URLPolicy = func(_ context.Context, u *url.URL) error {
		if strings.Contains(u.Path, "forbidden") {
			return blocked
		}
		return nil
	}
	p.Url = "http://example.test/forbidden/path"
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("blocked URL was sent")
		return nil, nil
	}), p)
	if !errors.Is(err, tn.ErrTargetBlocked) || !errors.Is(err, blocked) {
		t.Fatalf("err=%v", err)
	}
}

// TestScenarios_URLPolicyChecksRedirects 验证 URLPolicy 对每次重定向目标也执行检查。
func TestScenarios_URLPolicyChecksRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("disallowed target reached")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	p := params()
	p.Url = source.URL
	p.AllowCrossOriginRedirect = true
	p.URLPolicy = func(_ context.Context, u *url.URL) error {
		if u.String() != source.URL {
			return errors.New("redirect denied")
		}
		return nil
	}
	_, _, err := Api(source.Client(), p)
	if !errors.Is(err, tn.ErrTargetBlocked) {
		t.Fatalf("err=%v", err)
	}
}

// TestScenarios_NetworkPolicyBlockPrivateIPs 验证 NewPublicClient 强制检查实际拨号 IP，
// 拒绝访问内网地址。
func TestScenarios_NetworkPolicyBlockPrivateIPs(t *testing.T) {
	client, err := NewPublicClient(tn.ClientParam{SpaceName: t.Name()})
	if err != nil {
		t.Fatal(err)
	}
	p := params()
	p.Url = "http://127.0.0.1:1/private"
	_, _, err = Api(client, p)
	if !errors.Is(err, tn.ErrTargetBlocked) {
		t.Fatalf("local target allowed: %v", err)
	}
}

// TestScenarios_NetworkPolicyAllowedHosts 验证 AllowedHosts 进一步限制目标主机名，
// 不在白名单内的请求被拒绝。
func TestScenarios_NetworkPolicyAllowedHosts(t *testing.T) {
	client, err := NewPublicClient(tn.ClientParam{
		SpaceName:     t.Name(),
		NetworkPolicy: &tn.NetworkPolicy{AllowedHosts: []string{"allowed.example"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := params()
	p.Url = "http://denied.example/path"
	_, _, err = Api(client, p)
	if !errors.Is(err, tn.ErrTargetBlocked) {
		t.Fatalf("denied host allowed: %v", err)
	}
}

// TestScenarios_URLCredentialsRejected 验证 URL 中嵌入的 user:password 凭证被拒绝，
// 认证应放入 Authorization 头。
func TestScenarios_URLCredentialsRejected(t *testing.T) {
	p := params()
	p.Url = "http://user:secret@example.test/path"
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("URL with credentials was sent")
		return nil, nil
	}), p)
	if err == nil {
		t.Fatal("URL credentials accepted")
	}
}

// TestScenarios_HeaderInjectionRejected 验证请求头值中嵌入的 CRLF 字符被拒绝，
// 防止头注入攻击。
func TestScenarios_HeaderInjectionRejected(t *testing.T) {
	p := params()
	p.Header = map[string]string{"X-Test": "x\r\nInjected:yes"}
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("header injection sent")
		return nil, nil
	}), p)
	if err == nil {
		t.Fatal("header injection accepted")
	}
}

// TestScenarios_DuplicateHeaderRejected 验证大小写不同的同名头会被拒绝，
// 避免重复键造成歧义。
func TestScenarios_DuplicateHeaderRejected(t *testing.T) {
	p := params()
	p.Header = map[string]string{"x-test": "x", "X-Test": "y"}
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("duplicate header sent")
		return nil, nil
	}), p)
	if err == nil {
		t.Fatal("duplicate header accepted")
	}
}

// =====================================================================
// 10. 共享预算 Limiter
// =====================================================================

// TestScenarios_LimiterBoundsConcurrency 验证 Limiter 限制在途调用数，
// 超出 maxConcurrent 的请求会排队等待。
func TestScenarios_LimiterBoundsConcurrency(t *testing.T) {
	limiter, _ := tn.NewLimiter(2, 0)
	var active, peak atomic.Int32
	gate := make(chan struct{})
	entered := make(chan struct{}, 2)
	client := testClient(func(*http.Request) (*http.Response, error) {
		n := active.Add(1)
		for previous := peak.Load(); n > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, n) {
				break
			}
		}
		entered <- struct{}{}
		<-gate
		active.Add(-1)
		return response(200, "ok"), nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := params()
			p.Limiter = limiter
			if _, _, err := Api(client, p); err != nil {
				t.Error(err)
			}
		}()
	}
	<-entered
	<-entered
	close(gate)
	go func() {
		for i := 0; i < 4; i++ {
			<-entered
		}
	}()
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("peak=%d", peak.Load())
	}
}

// TestScenarios_LimiterAdmissionTooLarge 验证单次预算超过 Limiter 容量时直接拒绝，
// 不进入排队。
func TestScenarios_LimiterAdmissionTooLarge(t *testing.T) {
	limiter, _ := tn.NewLimiter(1, 1) // 缓冲容量仅 1 字节
	p := params()
	p.Limiter = limiter
	p.MaxResponseBytes = 1024 // 单次预算远超容量
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("oversized request admitted")
		return nil, nil
	}), p)
	if !errors.Is(err, tn.ErrAdmissionTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

// TestScenarios_LimiterHonorsCancellation 验证排队期间 Context 到期会立即退出，
// 不会在 Limiter 中无限等待。
func TestScenarios_LimiterHonorsCancellation(t *testing.T) {
	limiter, _ := tn.NewLimiter(1, 32<<20)
	release, err := limiter.Acquire(context.Background(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	p := params()
	p.Limiter = limiter
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err = ApiContext(ctx, testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("sent while queued")
		return nil, nil
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

// TestScenarios_NoDefaultLimiter 验证 nil 不再受到原先全局 256 MiB 预算的限制。
// 响应上限大于原默认预算，但模拟响应很小，因此不会为该上限预先分配内存。
func TestScenarios_NoDefaultLimiter(t *testing.T) {
	p := params()
	p.MaxResponseBytes = 512 << 20
	if _, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		return response(200, "ok"), nil
	}), p); err != nil {
		t.Fatal(err)
	}
}

// =====================================================================
// 11. 流式输出 ApiTo
// =====================================================================

// TestScenarios_ApiToWritesSuccessResponse 验证 ApiTo 将成功响应流式写入 dst，
// 返回实际写入字节数。
func TestScenarios_ApiToWritesSuccessResponse(t *testing.T) {
	p := params()
	var dst bytes.Buffer
	n, _, err := ApiTo(context.Background(), testClient(func(*http.Request) (*http.Response, error) {
		return response(200, "ok"), nil
	}), p, &dst)
	if err != nil || n != 2 || dst.String() != "ok" {
		t.Fatalf("n=%d dst=%q err=%v", n, dst.String(), err)
	}
}

// TestScenarios_ApiToRespectsLimit 验证 ApiTo 同样受 MaxResponseBytes 限制，
// 超限返回 ErrResponseTooLarge。
func TestScenarios_ApiToRespectsLimit(t *testing.T) {
	p := params()
	p.MaxResponseBytes = 4
	var dst bytes.Buffer
	n, _, err := ApiTo(context.Background(), testClient(func(*http.Request) (*http.Response, error) {
		return response(200, "12345"), nil
	}), p, &dst)
	if !errors.Is(err, tn.ErrResponseTooLarge) || n != 4 || dst.Len() != 4 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

// TestScenarios_ApiToRetriesBeforeWriting 验证 ApiTo 在写入任何字节前可重试，
// 例如首次返回 503 时会再次尝试。
func TestScenarios_ApiToRetriesBeforeWriting(t *testing.T) {
	p := params()
	p.Retry = 2
	calls := 0
	var dst bytes.Buffer
	n, _, err := ApiTo(context.Background(), testClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(503, "unavailable"), nil
		}
		return response(200, "ok"), nil
	}), p, &dst)
	if err != nil || calls != 2 || n != 2 || dst.String() != "ok" {
		t.Fatal(n, calls, err)
	}
}

// TestScenarios_ApiToNoRetryAfterWrite 验证一旦写入任何字节便不再重试，
// 调用方负责文件/事务的提交或回滚。
func TestScenarios_ApiToNoRetryAfterWrite(t *testing.T) {
	p := params()
	p.Retry = 2
	calls := 0
	var dst bytes.Buffer
	_, _, err := ApiTo(context.Background(), testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, "ok"), nil // 第一次就成功并写入
	}), p, &dst)
	if err != nil || calls != 1 || dst.String() != "ok" {
		t.Fatalf("calls=%d dst=%q err=%v", calls, dst.String(), err)
	}
}

// TestScenarios_ApiToWriterFailure 验证 dst 写入失败时返回 ErrResponseWrite 包装原始错误，
// 且不重试。
func TestScenarios_ApiToWriterFailure(t *testing.T) {
	cause := errors.New("disk full")
	p := params()
	p.Retry = 2
	p.RetryDecider = func(context.Context, tn.RetryInfo) bool { return true }
	calls := 0
	_, _, err := ApiTo(context.Background(), testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, "ok"), nil
	}), p, failingWriter{cause})
	if !errors.Is(err, tn.ErrResponseWrite) || !errors.Is(err, cause) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// =====================================================================
// 12. 客户端管理
// =====================================================================

// TestScenarios_NamedClientReuse 验证 NewClient 创建命名客户端，
// 同名且配置相符时复用已有实例。
func TestScenarios_NamedClientReuse(t *testing.T) {
	name := t.Name()
	first, err := NewClient(tn.ClientParam{SpaceName: name, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewClient(tn.ClientParam{SpaceName: name, Timeout: time.Second})
	if err != nil || second != first {
		t.Fatalf("reuse failed: first=%p second=%p err=%v", first, second, err)
	}
}

// TestScenarios_NamedClientConflictRejected 验证同名但配置不同的请求被拒绝，
// 避免配置冲突时静默复用错误实例。
func TestScenarios_NamedClientConflictRejected(t *testing.T) {
	name := t.Name()
	if _, err := NewClient(tn.ClientParam{SpaceName: name, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	_, err := NewClient(tn.ClientParam{SpaceName: name, Timeout: 2 * time.Second})
	if !errors.Is(err, tn.ErrClientConfig) {
		t.Fatalf("conflict not rejected: %v", err)
	}
}

// TestScenarios_GetClientExplicitLookup 验证 GetClient 显式取得已创建的命名客户端，
// 不存在时返回 ok=false。
func TestScenarios_GetClientExplicitLookup(t *testing.T) {
	name := t.Name()
	first, err := NewClient(tn.ClientParam{SpaceName: name, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := GetClient(name)
	if !ok || got != first {
		t.Fatalf("lookup failed: ok=%v got=%p first=%p", ok, got, first)
	}
	if _, ok := GetClient("nonexistent-name"); ok {
		t.Fatal("nonexistent client returned ok=true")
	}
}

// TestScenarios_ClientResetReplaces 验证 Reset=true 替换命名客户端，
// 关闭旧连接且不中断在途请求。
func TestScenarios_ClientResetReplaces(t *testing.T) {
	name := t.Name()
	first, err := NewClient(tn.ClientParam{SpaceName: name, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewClient(tn.ClientParam{SpaceName: name, Timeout: 2 * time.Second, Reset: true})
	if err != nil || second == first {
		t.Fatalf("reset failed: first=%p second=%p err=%v", first, second, err)
	}
}

// TestScenarios_NewClientUncachedIndependent 验证 NewClientUncached 每次创建独立客户端，
// 适合租户隔离和包含闭包的动态配置。
func TestScenarios_NewClientUncachedIndependent(t *testing.T) {
	fn := func(*http.Request, []*http.Request) error { return nil }
	first, err := NewClientUncached(tn.ClientParam{CheckRedirect: fn})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewClientUncached(tn.ClientParam{CheckRedirect: fn})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("uncached clients should be independent")
	}
	first.CloseIdleConnections()
	second.CloseIdleConnections()
}

// TestScenarios_NewClientTypedNilJarRejected 验证带类型的 nil Jar 被构造器拒绝，
// 避免后续请求因 nil panic。
func TestScenarios_NewClientTypedNilJarRejected(t *testing.T) {
	var jar *cookiejar.Jar
	for name, constructor := range map[string]func(tn.ClientParam) (*http.Client, error){
		"named":    NewClient,
		"uncached": NewClientUncached,
		"public":   NewPublicClient,
	} {
		t.Run(name, func(t *testing.T) {
			client, err := constructor(tn.ClientParam{SpaceName: t.Name(), Jar: jar})
			if !errors.Is(err, tn.ErrClientConfig) || client != nil {
				t.Fatalf("nil jar accepted: client=%p err=%v", client, err)
			}
		})
	}
}

// TestScenarios_NewClientRejectsInvalidConfig 验证构造器在创建阶段返回配置错误。
func TestScenarios_NewClientRejectsInvalidConfig(t *testing.T) {
	client, err := NewClient(tn.ClientParam{Timeout: -time.Second})
	if !errors.Is(err, tn.ErrClientConfig) || client != nil {
		t.Fatalf("invalid configuration returned client=%p err=%v", client, err)
	}
}

// =====================================================================
// 13. 日志脱敏
// =====================================================================

// TestScenarios_LoggingStartsWithMetadata 验证开启 Logging 后输出本次调用的开始日志，
// 包含方法、origin、request_id 等元数据，但不含 URL 路径/查询参数。
func TestScenarios_LoggingStartsWithMetadata(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	p := params()
	p.Url += "/secret-path?token=hidden"
	p.Logging = &logging.Config{Logger: logging.NewSlogLogger(logger)}
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
	log := output.String()
	if !strings.Contains(log, "http.call.start") {
		t.Fatalf("missing request log: %s", log)
	}
	if strings.Contains(log, "secret-path") || strings.Contains(log, "hidden") {
		t.Fatalf("URL path/query leaked: %s", log)
	}
}

// TestScenarios_LoggingEndsWithSummary 验证开启 Logging 后输出最终汇总日志，
// 包含状态、尝试次数和耗时。
func TestScenarios_LoggingEndsWithSummary(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	p := params()
	p.Logging = &logging.Config{Logger: logging.NewSlogLogger(logger)}
	p.Retry = 1
	p.RetryInterval = time.Nanosecond
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
	log := output.String()
	if !strings.Contains(log, "http.call.end") || !strings.Contains(log, "\"status\":200") {
		t.Fatalf("missing response log: %s", log)
	}
}

// TestScenarios_LogBodyRedactsSensitiveFields 验证开启正文采集时对 JSON 正文做脱敏，
// 内置 password/token 等字段被替换为 [REDACTED]。使用真实 httptest.Server 以确保正文被实际发送。
func TestScenarios_LogBodyRedactsSensitiveFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	payload, _ := json.Marshal(map[string]any{
		"username": "alice",
		"password": "secret-value",
		"token":    "tok",
	})
	reader, ct, _ := tc.ContentByJson(payload)
	p := params()
	p.Url = server.URL
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Type": ct}
	p.Logging = &logging.Config{Logger: logging.NewSlogLogger(logger), Capture: logging.CaptureOptions{RequestBody: true}}
	_, _, err := Api(server.Client(), p)
	if err != nil {
		t.Fatal(err)
	}
	log := output.String()
	if strings.Contains(log, "secret-value") || strings.Contains(log, "\"tok\"") {
		t.Fatalf("sensitive data leaked: %s", log)
	}
	if !strings.Contains(log, "[REDACTED]") {
		t.Fatalf("redaction missing: %s", log)
	}
}

// TestScenarios_LogBodyCustomSensitiveFields 验证 SensitiveFields 追加业务自定义字段，
// 与内置规则一起参与脱敏。
func TestScenarios_LogBodyCustomSensitiveFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	payload, _ := json.Marshal(map[string]any{"mobile": "13800000000", "id_card": "110"})
	reader, ct, _ := tc.ContentByJson(payload)
	p := params()
	p.Url = server.URL
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Type": ct}
	p.Logging = &logging.Config{Logger: logging.NewSlogLogger(logger), Capture: logging.CaptureOptions{RequestBody: true}}
	p.Logging.Redactor = logging.DefaultRedactor{SensitiveFields: []string{"mobile", "id_card"}}
	_, _, err := Api(server.Client(), p)
	if err != nil {
		t.Fatal(err)
	}
	log := output.String()
	if strings.Contains(log, "13800000000") || strings.Contains(log, "\"110\"") {
		t.Fatalf("custom sensitive data leaked: %s", log)
	}
}

// TestScenarios_RequestBodyLimitOmitsOversized 验证 RequestBodyLimit 限制正文预览长度，
// 超限或无法确认完整时输出 [omitted]，不会截取不完整片段。
func TestScenarios_RequestBodyLimitOmitsOversized(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	payload, _ := json.Marshal(map[string]any{"k": strings.Repeat("x", 1000)})
	reader, ct, _ := tc.ContentByJson(payload)
	p := params()
	p.Url = server.URL
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Type": ct}
	p.Logging = &logging.Config{Logger: logging.NewSlogLogger(logger), Capture: logging.CaptureOptions{RequestBody: true}}
	p.Logging.Capture.RequestBodyLimit = 64
	_, _, err := Api(server.Client(), p)
	if err != nil {
		t.Fatal(err)
	}
	log := output.String()
	if !strings.Contains(log, "[omitted]") {
		t.Fatalf("oversized body not omitted: %s", log)
	}
}

// TestScenarios_TraceIdAndCallerInLog 验证 TraceId 与 Caller 写入日志对应字段，
// 便于跨服务链路追踪。
func TestScenarios_TraceIdAndCallerInLog(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	p := params()
	p.Logging = &logging.Config{Logger: logging.NewSlogLogger(logger)}
	p.TraceId, p.Caller = "trace-xyz", "order.Create"
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		return response(200, "ok"), nil
	}), p)
	if err != nil {
		t.Fatal(err)
	}
	log := output.String()
	if !strings.Contains(log, "trace-xyz") || !strings.Contains(log, "order.Create") {
		t.Fatalf("trace/caller missing: %s", log)
	}
}

// =====================================================================
// 14. 压缩响应
// =====================================================================

// TestScenarios_GzipResponseTransparentlyDecoded 验证 Content-Encoding: gzip 的响应
// 被自动解压，调用方拿到的是解压后的正文。
func TestScenarios_GzipResponseTransparentlyDecoded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = io.WriteString(writer, "hello-gzip")
		_ = writer.Close()
	}))
	defer server.Close()
	p := params()
	p.Url = server.URL
	body, _, err := Api(server.Client(), p)
	if err != nil || string(body) != "hello-gzip" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

// TestScenarios_GzipLimitAppliesToDecoded 验证 MaxResponseBytes 对解压后的正文计数，
// 超限返回 ErrResponseTooLarge 并保留上限以内的部分。
func TestScenarios_GzipLimitAppliesToDecoded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = io.WriteString(writer, strings.Repeat("x", 8192))
		_ = writer.Close()
	}))
	defer server.Close()
	p := params()
	p.Url, p.MaxResponseBytes = server.URL, 64
	body, _, err := Api(server.Client(), p)
	if !errors.Is(err, tn.ErrResponseTooLarge) || len(body) != 64 {
		t.Fatalf("size=%d err=%v", len(body), err)
	}
}

// =====================================================================
// 15. HTTP/2 与 TLS
// =====================================================================

// TestScenarios_HTTP2Negotiated 验证 httptest TLS 服务器可协商 HTTP/2，
// 且默认客户端能正确处理。
func TestScenarios_HTTP2Negotiated(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Error("HTTP/2 not negotiated")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	p := params()
	p.Url = server.URL
	if body, _, err := Api(server.Client(), p); err != nil || string(body) != "ok" {
		t.Fatal(string(body), err)
	}
}

// TestScenarios_UntrustedTLSRejected 验证默认客户端拒绝不受信任的 TLS 证书，
// 不会因忽略证书校验而泄露请求。
func TestScenarios_UntrustedTLSRejected(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	p := params()
	p.Url = server.URL
	_, _, err := Api(nil, p) // 默认客户端不信任测试证书
	if err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
}

// =====================================================================
// 16. 中间件
// =====================================================================

// TestScenarios_CustomMiddlewareForPlainClient 验证自定义中间件
// 可用于普通 http.Client，不影响其基本请求能力。
func TestScenarios_CustomMiddlewareForPlainClient(t *testing.T) {
	client, err := NewClient(tn.ClientParam{
		SpaceName:   t.Name(),
		Transport:   roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, "ok"), nil }),
		Middlewares: []interfaces.Middleware{&clonedMiddleware{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get("http://example.test")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
}

// TestScenarios_MiddlewareCloneRequired 验证 Middlewares 中的节点必须实现 Cloner，
// 否则在创建客户端时被拒绝，避免共享可变节点造成并发问题。
func TestScenarios_MiddlewareCloneRequired(t *testing.T) {
	shared := &isolatedMiddleware{}
	_, err := NewClientUncached(tn.ClientParam{Middlewares: []interfaces.Middleware{shared}})
	if !errors.Is(err, tn.ErrClientConfig) {
		t.Fatalf("uncloneable node accepted: %v", err)
	}
}

// TestScenarios_MiddlewareFactoryIsolation 验证 MiddlewareFactories 构造的独立节点
// 不会因复用而相互污染。
func TestScenarios_MiddlewareFactoryIsolation(t *testing.T) {
	prototype := &clonedMiddleware{}
	_, err := NewClientUncached(tn.ClientParam{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(200, "ok"), nil
		}),
		Middlewares: []interfaces.Middleware{prototype},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prototype.next != nil {
		t.Fatal("prototype was mutated during construction")
	}
}

// =====================================================================
// 17. 参数校验
// =====================================================================

// TestScenarios_InvalidParametersNeverSend 验证无效参数在准备阶段被拒绝，
// 不会建立连接或发送请求。
func TestScenarios_InvalidParametersNeverSend(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(*tn.ApiParam)
	}{
		{"negative_retry", func(p *tn.ApiParam) { p.Retry = -1 }},
		{"retry_too_large", func(p *tn.ApiParam) { p.Retry = 101 }},
		{"negative_single_timeout", func(p *tn.ApiParam) { p.SingleTimeout = -1 }},
		{"negative_total_timeout", func(p *tn.ApiParam) { p.Timeout = -1 }},
		{"negative_retry_interval", func(p *tn.ApiParam) { p.RetryInterval = -1 }},
		{"negative_max_retry_delay", func(p *tn.ApiParam) { p.MaxRetryDelay = -1 }},
		{"max_redirects_too_large", func(p *tn.ApiParam) { p.MaxRedirects = 101 }},
		{"url_credentials", func(p *tn.ApiParam) { p.Url = "http://user:secret@example.test" }},
		{"url_no_scheme", func(p *tn.ApiParam) { p.Url = "example.test/path" }},
		{"url_file_scheme", func(p *tn.ApiParam) { p.Url = "file:///etc/passwd" }},
		{"invalid_method", func(p *tn.ApiParam) { p.Method = "WAT" }},
		{"invalid_header_name", func(p *tn.ApiParam) { p.Header = map[string]string{"Bad Header": "x"} }},
		{"header_injection", func(p *tn.ApiParam) { p.Header = map[string]string{"X-Test": "x\r\nInjected:yes"} }},
		{"duplicate_header", func(p *tn.ApiParam) { p.Header = map[string]string{"x-test": "x", "X-Test": "y"} }},
		{"content_length_mismatch", func(p *tn.ApiParam) {
			p.Body = bytes.NewReader([]byte("abc"))
			p.Header = map[string]string{"Content-Length": "2"}
		}},
		{"invalid_retry_status_code", func(p *tn.ApiParam) { p.RetryStatusCodes = []int{999} }},
		{"invalid_redirect_allowlist", func(p *tn.ApiParam) { p.RedirectHeaderAllowlist = []string{"Bad Header"} }},
		{"max_response_bytes_out_of_range", func(p *tn.ApiParam) {
			p.MaxResponseBytes = int64(^uint64(0) >> 1)
		}},
		{"log_body_limit_negative", func(p *tn.ApiParam) {
			p.Logging = &logging.Config{Capture: logging.CaptureOptions{RequestBodyLimit: -1}}
		}},
		{"invalid_limiter", func(p *tn.ApiParam) { p.Limiter = &tn.Limiter{} }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			p := params()
			mutate.fn(&p)
			_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid request sent")
				return nil, nil
			}), p)
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

// TestScenarios_NilContextRejected 验证 nil Context 被拒绝，不会 panic。
func TestScenarios_NilContextRejected(t *testing.T) {
	_, _, err := ApiContext(nil, testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("sent with nil context")
		return nil, nil
	}), params())
	if err == nil {
		t.Fatal("nil context accepted")
	}
}

// TestScenarios_DeclaredZeroLengthEmptyBodyAccepted 验证显式 Content-Length: 0
// 与空正文一致时被接受，且不会因探测而失败。
func TestScenarios_DeclaredZeroLengthEmptyBodyAccepted(t *testing.T) {
	p := params()
	p.Method = "POST"
	p.Body = strings.NewReader("")
	p.Header = map[string]string{"Content-Length": "0"}
	calls := 0
	_, _, err := Api(testClient(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Body != http.NoBody {
			t.Errorf("empty body not normalized: %T", req.Body)
		}
		return response(200, "ok"), nil
	}), p)
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestScenarios_DeclaredZeroLengthNonEmptyBodyRejected 验证显式 Content-Length: 0
// 但实际有数据时在发送前被拒绝。
func TestScenarios_DeclaredZeroLengthNonEmptyBodyRejected(t *testing.T) {
	p := params()
	p.Method = "POST"
	p.Body = strings.NewReader("unexpected")
	p.Header = map[string]string{"Content-Length": "0"}
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("nonempty body with declared zero length sent")
		return nil, nil
	}), p)
	if err == nil {
		t.Fatal("nonempty body with declared zero length accepted")
	}
}

// TestScenarios_TransferEncodingHeaderRejected 验证 Transfer-Encoding 头由 net/http 管理，
// 不能通过 Header 字段覆盖。
func TestScenarios_TransferEncodingHeaderRejected(t *testing.T) {
	p := params()
	p.Header = map[string]string{"Transfer-Encoding": "chunked"}
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("Transfer-Encoding override sent")
		return nil, nil
	}), p)
	if err == nil {
		t.Fatal("Transfer-Encoding override accepted")
	}
}

func TestK1(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"name": "test"})
	reader, ct, _ := tc.ContentByJson(payload)

	_, _, err := Api(nil, tn.ApiParam{
		Url:     "https://httpbin.org/post",
		Method:  "POST",
		Header:  map[string]string{"Content-Type": ct},
		Caller:  "k1.test",
		Body:    reader,
		Logging: &logging.Config{},
	})
	fmt.Println(err != nil)
}
