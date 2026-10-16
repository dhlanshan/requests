package mdw

import (
	"net/http"

	"github.com/dhlanshan/requests/interfaces"
	"github.com/dhlanshan/requests/internal/runner"
	"github.com/dhlanshan/requests/tn"
)

// RetryData 保留已有类型及总线读取方式。
type RetryData = tn.RetryData

// RetryMiddleware 保留兼容节点。重试由 Api/ApiContext 执行，避免中间件
// 顺序、重定向和自定义 Client 导致重复重试或跳过校验。
type RetryMiddleware struct{ Transport http.RoundTripper }

func (mw *RetryMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	if meta, ok := req.Context().Value(tn.CtxJKExtend{}).(*tn.ApiParam); ok && meta != nil && !runner.Managed(req.Context()) {
		return runner.LegacyRoundTrip(req, *meta, nextTransport(mw.Transport))
	}
	return nextTransport(mw.Transport).RoundTrip(req)
}
func (mw *RetryMiddleware) SetTransport(rt http.RoundTripper) { mw.Transport = rt }
func (mw *RetryMiddleware) Name() string                      { return "Retry Middleware" }
func (mw *RetryMiddleware) Clone() interfaces.Middleware      { return &RetryMiddleware{} }
func (mw *RetryMiddleware) CloseIdleConnections()             { closeIdle(nextTransport(mw.Transport)) }
