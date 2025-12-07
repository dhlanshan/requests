package mdw

import (
	"github.com/dhlanshan/requests/interfaces"
	"github.com/dhlanshan/requests/internal/utils"
	"net/http"
)

// MiddlewareChain 中间件链管理器
type MiddlewareChain struct {
	Middlewares []interfaces.Middleware
	transport   http.RoundTripper
}

func NewMdwChain(transport http.RoundTripper) *MiddlewareChain {
	transport = utils.TernaryOperator(transport == nil, http.DefaultTransport, transport)
	return &MiddlewareChain{
		Middlewares: []interfaces.Middleware{},
		transport:   transport,
	}
}

func (mc *MiddlewareChain) Add(middlewares ...interfaces.Middleware) http.RoundTripper {
	for _, m := range middlewares {
		mc.Middlewares = append(mc.Middlewares, m)
	}

	return mc.buildChain()
}

func (mc *MiddlewareChain) buildChain() http.RoundTripper {
	transport := mc.transport

	for i := len(mc.Middlewares) - 1; i >= 0; i-- {
		middleware := mc.Middlewares[i]
		middleware.SetTransport(transport)
		transport = middleware
	}

	return transport
}
