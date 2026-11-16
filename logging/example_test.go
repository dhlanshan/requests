package logging_test

import (
	"context"
	"fmt"
	"time"

	"github.com/dhlanshan/requests/logging"
)

// ExampleStart 展示 logging 可以独立使用：外部执行器也能驱动同一套日志协议。
// requests 已经自动派发生命周期，正常使用 Api 系列时无需手工调用 Start/OnEnd。
func ExampleStart() {
	ctx := context.Background()
	config := &logging.Config{
		Logger: logging.LoggerFunc(func(_ context.Context, info logging.CallInfo) logging.Session {
			return logging.SessionFuncs{
				End: func(_ context.Context, result logging.CallResult) {
					fmt.Printf("request=%s trace=%s retries=%d duration=%s\n",
						info.RequestID, info.TraceID, result.RetryCount, result.Duration)
				},
			}
		}),
	}
	call := logging.Start(ctx, config, logging.CallInfo{
		RequestID: "request-1", TraceID: "trace-1", Method: "GET", URL: "https://example.test",
	})
	call.OnEnd(ctx, logging.CallResult{
		AttemptCount: 2, RetryCount: 1, Duration: 100 * time.Millisecond,
		StatusCode: 200, Phase: logging.PhaseComplete,
	})
	// Output: request=request-1 trace=trace-1 retries=1 duration=100ms
}
