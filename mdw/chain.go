package mdw

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"github.com/dhlanshan/requests/interfaces"
	"github.com/dhlanshan/requests/internal/utils"
)

// MiddlewareChain 仅在构建阶段使用；开始发送请求后不要再调用 Add/Build。
// Middlewares 必须实现 Cloner；也可使用 Factories 构造独立节点。
type MiddlewareChain struct {
	Middlewares []interfaces.Middleware
	Factories   []interfaces.MiddlewareFactory
	transport   http.RoundTripper
}

func nextTransport(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		return http.DefaultTransport
	}
	if utils.IsNil(rt) {
		return ErrorTransport{Err: errors.New("nil transport")}
	}
	return rt
}
func closeIdle(rt http.RoundTripper) {
	if closer, ok := rt.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
func NewMdwChain(transport http.RoundTripper) *MiddlewareChain {
	return &MiddlewareChain{transport: nextTransport(transport)}
}
func (mc *MiddlewareChain) Add(middlewares ...interfaces.Middleware) http.RoundTripper {
	mc.Middlewares = append(mc.Middlewares, middlewares...)
	rt, err := mc.Build()
	if err != nil {
		return ErrorTransport{Err: err}
	}
	return rt
}

// Build 在修改任何链节点前检查 nil 和重复实例，避免形成自引用死循环。
func (mc *MiddlewareChain) Build() (http.RoundTripper, error) {
	seen := make(map[interfaces.Middleware]bool)
	seenNodes := make(map[interfaces.Middleware]bool)
	nodes := make([]interfaces.Middleware, 0, len(mc.Middlewares))
	for _, mw := range mc.Middlewares {
		if mw == nil {
			return nil, errors.New("nil middleware")
		}
		value := reflect.ValueOf(mw)
		switch value.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			if value.IsNil() {
				return nil, errors.New("nil middleware")
			}
		}
		if !value.Type().Comparable() {
			return nil, errors.New("middleware must have comparable identity; use a pointer")
		}
		if seen[mw] {
			return nil, fmt.Errorf("duplicate middleware: %s", mw.Name())
		}
		seen[mw] = true
		if cloner, ok := mw.(interfaces.Cloner); ok {
			mw = cloner.Clone()
			if utils.IsNil(mw) {
				return nil, errors.New("middleware Clone returned nil")
			}
			if reflect.TypeOf(mw).Comparable() && seen[mw] {
				return nil, errors.New("middleware Clone must return an independent node")
			}
		} else {
			return nil, errors.New("middleware must implement interfaces.Cloner; alternatively use MiddlewareFactories")
		}
		if !reflect.TypeOf(mw).Comparable() {
			return nil, errors.New("cloned middleware must have comparable identity")
		}
		if seenNodes[mw] || http.RoundTripper(mw) == mc.transport {
			return nil, errors.New("middleware chain contains a cycle")
		}
		seenNodes[mw] = true
		nodes = append(nodes, mw)
	}
	rt := nextTransport(mc.transport)
	for i := len(nodes) - 1; i >= 0; i-- {
		nodes[i].SetTransport(rt)
		rt = nodes[i]
	}
	// 工厂列表从外到内包裹传统节点；构造在缓存锁外执行。
	for i := len(mc.Factories) - 1; i >= 0; i-- {
		factory := mc.Factories[i]
		if factory == nil {
			return nil, errors.New("nil middleware factory")
		}
		next, err := factory(rt)
		if err != nil {
			return nil, err
		}
		if utils.IsNil(next) {
			return nil, errors.New("middleware factory returned nil")
		}
		rt = next
	}
	return &chainTransport{RoundTripper: rt, base: nextTransport(mc.transport)}, nil
}

// ErrorTransport 让保留签名的 NewClient/Add 在配置错误时安全失败，绝不发出请求。
type ErrorTransport struct{ Err error }

func (t ErrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return nil, t.Err
}

type chainTransport struct {
	http.RoundTripper
	base http.RoundTripper
}

func (t *chainTransport) CloseIdleConnections() {
	if closer, ok := t.RoundTripper.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
		return
	}
	closeIdle(t.base)
}
