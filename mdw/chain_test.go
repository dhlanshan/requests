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

type aliasedClone struct {
	target interfaces.Middleware
	id     int
}

func (m *aliasedClone) SetTransport(http.RoundTripper)                  {}
func (m *aliasedClone) Name() string                                    { return "alias" }
func (m *aliasedClone) Clone() interfaces.Middleware                    { return m.target }
func (m *aliasedClone) RoundTrip(*http.Request) (*http.Response, error) { panic("not used") }

func TestClonedNodesCannotFormCycle(t *testing.T) {
	shared := &RetryMiddleware{}
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
	var nilMiddleware *RetryMiddleware
	for _, nodes := range [][]interfaces.Middleware{{nil}, {nilMiddleware}} {
		chain := NewMdwChain(base)
		chain.Middlewares = nodes
		if _, err := chain.Build(); err == nil {
			t.Fatal("nil middleware accepted")
		}
	}
	shared := &RetryMiddleware{}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rt := NewMdwChain(base).Add(shared, &LoggingMiddleware{})
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
	if shared.Transport != nil {
		t.Fatal("shared prototype was mutated")
	}
	if base.closed.Load() != 32 {
		t.Fatalf("idle close was not forwarded: %d", base.closed.Load())
	}
}
