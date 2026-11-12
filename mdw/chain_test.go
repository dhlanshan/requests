package mdw

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dhlanshan/requests/interfaces"
)

type baseTransport struct{ closed atomic.Int32 }

type passthroughMiddleware struct{ transport http.RoundTripper }

func (m *passthroughMiddleware) SetTransport(rt http.RoundTripper) { m.transport = rt }
func (m *passthroughMiddleware) Name() string                      { return "passthrough" }
func (m *passthroughMiddleware) Clone() interfaces.Middleware      { return &passthroughMiddleware{} }
func (m *passthroughMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.transport.RoundTrip(req)
}
func (m *passthroughMiddleware) CloseIdleConnections() { closeIdle(m.transport) }

type aliasedClone struct {
	target interfaces.Middleware
	id     int
}

func (m *aliasedClone) SetTransport(http.RoundTripper)                  {}
func (m *aliasedClone) Name() string                                    { return "alias" }
func (m *aliasedClone) Clone() interfaces.Middleware                    { return m.target }
func (m *aliasedClone) RoundTrip(*http.Request) (*http.Response, error) { panic("not used") }

func TestClonedNodesCannotFormCycle(t *testing.T) {
	shared := &passthroughMiddleware{}
	chain := NewMdwChain(&baseTransport{})
	chain.Middlewares = []interfaces.Middleware{&aliasedClone{target: shared, id: 1}, &aliasedClone{target: shared, id: 2}}
	if _, err := chain.Build(); err == nil {
		t.Fatal("clones that alias each other accepted")
	}
}

func (*baseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
}
func (b *baseTransport) CloseIdleConnections() { b.closed.Add(1) }

func TestMiddlewareValidationAndIndependentChains(t *testing.T) {
	base := &baseTransport{}
	var nilMiddleware *passthroughMiddleware
	for _, nodes := range [][]interfaces.Middleware{{nil}, {nilMiddleware}} {
		chain := NewMdwChain(base)
		chain.Middlewares = nodes
		if _, err := chain.Build(); err == nil {
			t.Fatal("nil middleware accepted")
		}
	}
	shared := &passthroughMiddleware{}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chain := NewMdwChain(base)
			chain.Middlewares = []interfaces.Middleware{shared}
			rt, err := chain.Build()
			if err != nil {
				t.Error(err)
				return
			}
			client := &http.Client{Transport: rt}
			resp, err := client.Get("http://example.test")
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			client.CloseIdleConnections()
		}()
	}
	wg.Wait()
	if shared.transport != nil {
		t.Fatal("shared prototype was mutated")
	}
	if base.closed.Load() != 32 {
		t.Fatalf("idle close was not forwarded: %d", base.closed.Load())
	}
}

func TestBuildRejectsTypedNilTransport(t *testing.T) {
	var transport *baseTransport
	if rt, err := NewMdwChain(transport).Build(); err == nil || rt != nil {
		t.Fatalf("typed-nil transport accepted: transport=%v err=%v", rt, err)
	}
}
