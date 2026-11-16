package requests_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dhlanshan/requests"
	"github.com/dhlanshan/requests/logging"
	"github.com/dhlanshan/requests/tn"
)

// exampleLoggingTransport 只在示例中模拟服务端，不访问外部网络。
type exampleLoggingTransport func(*http.Request) (*http.Response, error)

func (f exampleLoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// ExampleApiContext_customLogging 展示第三方调用者如何替换日志，并得到每次重试的正文与错误。
// Logger 可以共享；Start 内创建的 Session/闭包状态只属于本次调用。
func ExampleApiContext_customLogging() {
	config := &logging.Config{
		Capture: logging.CaptureOptions{
			RequestHeaders: true, ResponseHeaders: true,
			RequestBody: true, ResponseBody: true,
		},
		Logger: logging.LoggerFunc(func(_ context.Context, info logging.CallInfo) logging.Session {
			return logging.SessionFuncs{
				AttemptEnd: func(_ context.Context, result logging.AttemptResult) {
					status := 0
					var responseBody []byte
					if result.Response != nil {
						status = result.Response.StatusCode
						responseBody = result.Response.Body.Data
					}
					// 可替换为公司的日志方法。这里的正文和 Header 已经默认脱敏；
					// result.Err 可只读分类，result.Duration 是该次尝试耗时。
					fmt.Printf("attempt=%d status=%d failed=%t request=%s response=%s\n",
						result.Attempt, status, result.Err != nil, result.Request.Body.Data, responseBody)
				},
				Retry: func(_ context.Context, plan logging.RetryEvent) {
					fmt.Printf("retry after attempt=%d next=%d\n", plan.Attempt, plan.NextAttempt)
				},
				End: func(_ context.Context, result logging.CallResult) {
					// result.Duration 是含重试等待的整次调用耗时。
					fmt.Printf("track_id=%s attempts=%d retries=%d failed=%t\n",
						info.TraceID, result.AttemptCount, result.RetryCount, result.Err != nil)
				},
			}
		}),
	}

	sent := 0
	client := &http.Client{Transport: exampleLoggingTransport(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		_, _ = io.Copy(io.Discard, req.Body)
		sent++
		status, body := 503, "{\"code\":\"busy\"}"
		if sent == 2 {
			status, body = 200, "{\"ok\":true}"
		}
		return &http.Response{
			StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
			ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	_, _, err := requests.ApiContext(context.Background(), client, tn.ApiParam{
		Url: "https://example.test/orders", Method: http.MethodGet,
		Body:   strings.NewReader("{\"order_id\":42,\"password\":\"secret\"}"),
		Header: map[string]string{"Content-Type": "application/json"},
		Retry:  1, RetryInterval: time.Nanosecond,
		TraceId: "trace-demo", Logging: config,
	})
	if err != nil {
		panic(err)
	}
	// Output:
	// attempt=1 status=503 failed=true request={"order_id":42,"password":"[REDACTED]"} response={"code":"busy"}
	// retry after attempt=1 next=2
	// attempt=2 status=200 failed=false request={"order_id":42,"password":"[REDACTED]"} response={"ok":true}
	// track_id=trace-demo attempts=2 retries=1 failed=false
}
