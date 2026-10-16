package mdw

import (
	"net/http"

	"github.com/dhlanshan/requests/interfaces"
	"github.com/dhlanshan/requests/internal/runner"
	"github.com/dhlanshan/requests/tn"
)

// LoggingMiddleware 保留兼容节点；安全日志集中在 Api/ApiContext，
// 不依赖 RetryMiddleware 或私有 Context，普通 Client.Get 也可安全使用。
type LoggingMiddleware struct{ Transport http.RoundTripper }

func (mw *LoggingMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	if meta, ok := req.Context().Value(tn.CtxJKExtend{}).(*tn.ApiParam); ok && meta != nil && !runner.Managed(req.Context()) {
		return runner.LegacyRoundTrip(req, *meta, nextTransport(mw.Transport))
	}
	return nextTransport(mw.Transport).RoundTrip(req)
}
func (mw *LoggingMiddleware) SetTransport(rt http.RoundTripper) { mw.Transport = rt }
func (mw *LoggingMiddleware) Name() string                      { return "Logging Middleware" }
func (mw *LoggingMiddleware) Clone() interfaces.Middleware      { return &LoggingMiddleware{} }
func (mw *LoggingMiddleware) CloseIdleConnections()             { closeIdle(nextTransport(mw.Transport)) }
