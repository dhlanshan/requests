package interfaces

import "net/http"

// Middleware 在建链时绑定下游。RoundTrip 必须遵守 net/http 的并发及 Body 关闭约定。
type Middleware interface {
	http.RoundTripper
	SetTransport(rt http.RoundTripper)
	Name() string
}

// Cloner 返回独立的新节点，避免同一个实例被不同 Client 改写下游。
type Cloner interface{ Clone() Middleware }

// MiddlewareFactory 每次构造独立节点；next 不可在构造后改写。
// 返回的 RoundTripper 必须支持并发请求，不能返回跨客户端共享的可变链节点。
type MiddlewareFactory func(next http.RoundTripper) (http.RoundTripper, error)
