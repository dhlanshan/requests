package core

import (
	"errors"
	"net/http"
	"testing"

	"github.com/dhlanshan/requests/interfaces"
	"github.com/dhlanshan/requests/tn"
)

// 通过构造 Client 时的中间件工厂观察实际交给链的 Transport，而不只测试配置字段。
func TestClientConnectionLimitsAreExplicit(t *testing.T) {
	for _, limit := range []int{0, 7, 256} {
		var built *http.Transport
		client, err := NewClientUncached(tn.ClientParam{
			MaxConnsPerHost: limit,
			MiddlewareFactories: []interfaces.MiddlewareFactory{
				func(next http.RoundTripper) (http.RoundTripper, error) {
					built, _ = next.(*http.Transport)
					return next, nil
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer client.CloseIdleConnections()
		if built == nil || built.MaxConnsPerHost != limit || built.MaxIdleConns <= 0 || built.MaxIdleConnsPerHost <= 0 || !built.ForceAttemptHTTP2 {
			t.Fatalf("limit=%d actual transport=%+v", limit, built)
		}
	}
	base, ok := NewDefaultClient().Transport.(*http.Transport)
	if !ok || base.MaxConnsPerHost != 0 {
		t.Fatal("shared default client applied an implicit connection limit", base)
	}
	if NewDefaultClient() != NewDefaultClient() {
		t.Fatal("default client stopped reusing its connection pool")
	}
}

func TestCachedClientDistinguishesUnlimitedAndExplicitConnections(t *testing.T) {
	name := t.Name()
	unlimited, err := NewClient(tn.ClientParam{SpaceName: name})
	if err != nil {
		t.Fatal(err)
	}
	defer unlimited.CloseIdleConnections()
	reused, err := NewClient(tn.ClientParam{SpaceName: name})
	if err != nil || reused != unlimited {
		t.Fatal("equivalent unlimited configuration did not reuse the client", err)
	}
	if client, err := NewClient(tn.ClientParam{SpaceName: name, MaxConnsPerHost: 256}); !errors.Is(err, tn.ErrClientConfig) || client != nil {
		t.Fatal("unlimited and 256 connections were treated as equivalent", client, err)
	}
}

func TestCustomTransportRetainsItsConnectionPolicy(t *testing.T) {
	custom := &http.Transport{MaxConnsPerHost: 3}
	var built http.RoundTripper
	client, err := NewClientUncached(tn.ClientParam{
		Transport: custom, MaxConnsPerHost: 9,
		MiddlewareFactories: []interfaces.MiddlewareFactory{
			func(next http.RoundTripper) (http.RoundTripper, error) {
				built = next
				return next, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if built != custom || custom.MaxConnsPerHost != 3 {
		t.Fatal("the library changed the caller's transport", built, custom)
	}
}
