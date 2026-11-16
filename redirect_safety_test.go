package requests

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhlanshan/requests/tn"
)

// 实际检查服务端收到的头，覆盖返回原站及外站内继续跳转两种边界。
func TestRedirectHeadersStayFilteredOnReturnToInitialOrigin(t *testing.T) {
	for _, test := range []struct {
		name         string
		sameOrigin   bool
		privateHop   bool
		allowReferer bool
		denyCross    bool
	}{
		{name: "same_origin", sameOrigin: true},
		{name: "return_directly"},
		{name: "return_after_foreign_hop", privateHop: true},
		{name: "referer_explicitly_allowed", privateHop: true, allowReferer: true},
		{name: "cross_origin_denied", privateHop: true, denyCross: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var foreignCalls atomic.Int32
			returnURL := make(chan string, 1)
			finalHeaders := make(chan http.Header, 1)
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				foreignCalls.Add(1)
				if value := r.Header.Get("X-Initial-Secret"); value != "" {
					t.Errorf("initial credential reached foreign hop %s: %q", r.URL.Path, value)
				}
				if test.privateHop && r.URL.Path == "/entry" {
					http.Redirect(w, r, "/private?token=foreign-secret", http.StatusFound)
					return
				}
				http.Redirect(w, r, <-returnURL+"/final", http.StatusFound)
			}))
			defer foreign.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					if r.Header.Get("X-Initial-Secret") != "initial-secret" {
						t.Error("initial request lost its credential")
					}
					target := foreign.URL + "/entry"
					if test.sameOrigin {
						target = "/final"
					}
					http.Redirect(w, r, target, http.StatusFound)
					return
				}
				finalHeaders <- r.Header.Clone()
				_, _ = io.WriteString(w, "ok")
			}))
			defer origin.Close()
			returnURL <- origin.URL
			transport := &http.Transport{}
			defer transport.CloseIdleConnections()
			p := params()
			p.Url, p.Timeout = origin.URL+"/start", 3*time.Second
			p.Header = map[string]string{"X-Initial-Secret": "initial-secret"}
			p.AllowCrossOriginRedirect = !test.denyCross
			if test.allowReferer {
				p.RedirectHeaderAllowlist = []string{"Referer"}
			}
			_, _, err := Api(&http.Client{Transport: transport}, p)
			if test.denyCross {
				if !errors.Is(err, tn.ErrTargetBlocked) || foreignCalls.Load() != 0 {
					t.Fatalf("foreign calls=%d err=%v", foreignCalls.Load(), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			header := <-finalHeaders
			wantReferer, wantSecret, wantForeign := "", "", int32(1)
			if test.privateHop {
				wantForeign = 2
			}
			if test.sameOrigin {
				wantReferer, wantSecret, wantForeign = origin.URL+"/start", "initial-secret", 0
			} else if test.allowReferer {
				wantReferer = foreign.URL + "/private?token=foreign-secret"
			}
			if header.Get("Referer") != wantReferer || header.Get("X-Initial-Secret") != wantSecret || foreignCalls.Load() != wantForeign {
				t.Fatalf("final headers=%v foreign calls=%d want referer=%q secret=%q calls=%d", header, foreignCalls.Load(), wantReferer, wantSecret, wantForeign)
			}
		})
	}
}

func TestRedirectRetryRequiresIdempotencyAtEveryUnsafeHop(t *testing.T) {
	for _, test := range []struct {
		name         string
		key          string
		keepKey      bool
		unsafe       bool
		intermediate bool
		networkError bool
		wantAttempts int
	}{
		{name: "stripped_key", wantAttempts: 1},
		{name: "allowed_key", keepKey: true, wantAttempts: 2},
		{name: "explicit_unsafe", unsafe: true, wantAttempts: 2},
		{name: "unsafe_intermediate_before_get", intermediate: true, wantAttempts: 1},
		{name: "network_error_after_key_removal", networkError: true, wantAttempts: 1},
		{name: "x_stripped_key", key: "X-Idempotency-Key", wantAttempts: 1},
		{name: "x_allowed_key", key: "X-Idempotency-Key", keepKey: true, wantAttempts: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := test.key
			if key == "" {
				key = "Idempotency-Key"
			}
			originCalls, targetCalls, finalCalls, decisions := 0, 0, 0, 0
			client := testClient(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "origin.test":
					originCalls++
					if req.Header.Get(key) != "operation-1" {
						t.Error("initial request lost its idempotency key")
					}
					res := response(307, "")
					res.Header.Set("Location", "https://target.test/action")
					return res, nil
				case "target.test":
					targetCalls++
					payload, readErr := io.ReadAll(req.Body)
					if readErr != nil || string(payload) != "operation" {
						t.Fatalf("redirected payload=%q err=%v", payload, readErr)
					}
					if req.Method != "POST" || (req.Header.Get(key) != "") != test.keepKey {
						t.Errorf("method=%s key=%q", req.Method, req.Header.Get(key))
					}
					if test.networkError {
						return nil, io.ErrUnexpectedEOF
					}
					if test.intermediate {
						res := response(302, "")
						res.Header.Set("Location", "https://final.test/result")
						return res, nil
					}
					if targetCalls == 1 {
						return response(503, "retry later"), nil
					}
					return response(200, "ok"), nil
				default:
					finalCalls++
					if req.Method != "GET" {
						t.Errorf("final method=%s", req.Method)
					}
					return response(503, "retry later"), nil
				}
			})
			p := params()
			p.Url, p.Method = "https://origin.test/action", "POST"
			p.Body = strings.NewReader("operation")
			p.Retry, p.AllowUnsafeRetry = 1, test.unsafe
			p.Header = map[string]string{key: "operation-1"}
			p.AllowCrossOriginRedirect, p.AllowCrossOriginBody = true, true
			p.RetryDecider = func(context.Context, tn.RetryInfo) bool { decisions++; return true }
			if test.keepKey {
				p.RedirectHeaderAllowlist = []string{key}
			}
			_, _, err := Api(client, p)
			if originCalls != test.wantAttempts || targetCalls != test.wantAttempts {
				t.Fatalf("origin=%d target=%d want=%d err=%v", originCalls, targetCalls, test.wantAttempts, err)
			}
			if test.wantAttempts == 1 {
				if err == nil || decisions != 0 {
					t.Fatalf("unsafe redirect reached retry decider: decisions=%d err=%v", decisions, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if test.intermediate && finalCalls != 1 {
				t.Fatalf("final requests=%d", finalCalls)
			}
			if test.networkError && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("lost network error: %v", err)
			}
		})
	}
}

func TestRedirectDowngradeChecksPreviousHop(t *testing.T) {
	for _, allow := range []bool{false, true} {
		name := "denied"
		if allow {
			name = "allowed"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			client := testClient(func(req *http.Request) (*http.Response, error) {
				calls++
				res := response(200, "ok")
				res.Request = req
				switch req.URL.Path {
				case "/start":
					res.StatusCode = 302
					res.Header.Set("Location", "https://service.test/secure")
				case "/secure":
					res.StatusCode = 302
					res.Header.Set("Location", "http://service.test/final")
				}
				return res, nil
			})
			p := params()
			p.Url = "http://service.test/start"
			p.AllowCrossOriginRedirect, p.AllowHTTPSDowngrade = true, allow
			_, _, err := Api(client, p)
			if allow {
				if err != nil || calls != 3 {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			} else if !errors.Is(err, tn.ErrTargetBlocked) || calls != 2 {
				t.Fatalf("downgrade was sent: calls=%d err=%v", calls, err)
			}
		})
	}
}
