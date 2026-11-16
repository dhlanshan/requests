package runner

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dhlanshan/requests/tn"
)

// 使用内存响应隔离网络波动，比较不配置、仅限制并发和同时限制容量时的执行开销。
// 较多工作协程同时复用 Client，覆盖 Limiter 争用，而不是只测串行的申请/释放。
func BenchmarkExecuteLimiter(b *testing.B) {
	for _, mode := range []string{"Unconfigured", "ConcurrencyOnly", "WithBufferBudget"} {
		b.Run(mode, func(b *testing.B) {
			p := tn.ApiParam{Url: "https://example.test", Method: http.MethodGet}
			switch mode {
			case "ConcurrencyOnly":
				p.Limiter, _ = tn.NewLimiter(64, 0)
			case "WithBufferBudget":
				p.Limiter, _ = tn.NewLimiter(64, 256<<20)
			}
			client := &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, ContentLength: 2, Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})}
			b.ReportAllocs()
			b.SetParallelism(4)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, _, err := Execute(context.Background(), client, p); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
