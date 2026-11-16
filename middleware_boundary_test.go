package requests

import (
	"errors"
	"net/http"
	"testing"

	"github.com/dhlanshan/requests/interfaces"
	"github.com/dhlanshan/requests/tn"
)

type comparableBoundaryMiddleware struct {
	metadata any
	next     *http.RoundTripper
	bound    *int
}

func (m comparableBoundaryMiddleware) Name() string { return "comparable-boundary" }
func (m comparableBoundaryMiddleware) SetTransport(next http.RoundTripper) {
	*m.next = next
	if m.bound != nil {
		*m.bound += 1
	}
}
func (m comparableBoundaryMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	return (*m.next).RoundTrip(req)
}
func (m comparableBoundaryMiddleware) Clone() interfaces.Middleware {
	var next http.RoundTripper
	return &comparableBoundaryMiddleware{metadata: m.metadata, next: &next, bound: m.bound}
}

type fixedBoundaryClone struct{ node interfaces.Middleware }

func (*fixedBoundaryClone) Name() string                   { return "fixed-clone" }
func (*fixedBoundaryClone) SetTransport(http.RoundTripper) {}
func (m *fixedBoundaryClone) Clone() interfaces.Middleware { return m.node }
func (*fixedBoundaryClone) RoundTrip(*http.Request) (*http.Response, error) {
	panic("invalid configuration must not send a request")
}

func TestMiddlewareActualValueValidation(t *testing.T) {
	for constructorName, constructor := range map[string]func(tn.ClientParam) (*http.Client, error){
		"named": NewClient, "uncached": NewClientUncached,
	} {
		for _, kind := range []string{"slice_value", "map_value", "slice_clone", "map_clone", "nil_prototype", "nil_clone", "pointer_with_slice", "comparable_value"} {
			t.Run(constructorName+"/"+kind, func(t *testing.T) {
				defer func() {
					if value := recover(); value != nil {
						t.Fatalf("configuration validation panicked: %v", value)
					}
				}()
				var next http.RoundTripper
				node := comparableBoundaryMiddleware{metadata: []string{"business"}, next: &next}
				var prototype interfaces.Middleware = node
				valid := false
				switch kind {
				case "map_value":
					node.metadata = map[string]string{"key": "value"}
					prototype = node
				case "slice_clone":
					prototype = &fixedBoundaryClone{node: node}
				case "map_clone":
					node.metadata = map[string]string{"key": "value"}
					prototype = &fixedBoundaryClone{node: node}
				case "nil_prototype":
					var nilNode *comparableBoundaryMiddleware
					prototype = nilNode
				case "nil_clone":
					var nilNode *comparableBoundaryMiddleware
					prototype = &fixedBoundaryClone{node: nilNode}
				case "pointer_with_slice":
					prototype, valid = &node, true
				case "comparable_value":
					node.metadata = "business"
					prototype, valid = node, true
				}
				bound := 0
				first := &comparableBoundaryMiddleware{next: new(http.RoundTripper), bound: &bound}
				calls := 0
				client, err := constructor(tn.ClientParam{SpaceName: t.Name(),
					Transport:   roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return response(200, "ok"), nil }),
					Middlewares: []interfaces.Middleware{first, prototype}})
				if !valid {
					if !errors.Is(err, tn.ErrClientConfig) || client != nil || bound != 0 {
						t.Fatalf("invalid configuration: client=%p err=%v bound=%d", client, err, bound)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				defer client.CloseIdleConnections()
				body, _, err := Api(client, params())
				if err != nil || string(body) != "ok" || calls != 1 || bound != 1 {
					t.Fatalf("valid middleware: body=%q calls=%d bound=%d err=%v", body, calls, bound, err)
				}
			})
		}
	}
}
