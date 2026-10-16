package requests

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dhlanshan/requests/mdw"
	"github.com/dhlanshan/requests/tn"
)

func TestRedirectRetryRequiresIdempotencyAtEveryUnsafeHop(t *testing.T) {
	for _, test := range []struct {
		name         string
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
	} {
		t.Run(test.name, func(t *testing.T) {
			originCalls, targetCalls, finalCalls, decisions := 0, 0, 0, 0
			client := testClient(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "origin.test":
					originCalls++
					if req.Header.Get("Idempotency-Key") != "operation-1" {
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
					if req.Method != "POST" || (req.Header.Get("Idempotency-Key") != "") != test.keepKey {
						t.Errorf("method=%s key=%q", req.Method, req.Header.Get("Idempotency-Key"))
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
			p.Header = map[string]string{"Idempotency-Key": "operation-1"}
			p.AllowCrossOriginRedirect, p.AllowCrossOriginBody = true, true
			p.RetryDecider = func(context.Context, tn.RetryInfo) bool { decisions++; return true }
			if test.keepKey {
				p.RedirectHeaderAllowlist = []string{"Idempotency-Key"}
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
	for _, legacy := range []bool{false, true} {
		for _, allow := range []bool{false, true} {
			name := "api"
			if legacy {
				name = "legacy"
			}
			if allow {
				name += "_allowed"
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
				var err error
				if legacy {
					client.Transport = &mdw.RetryMiddleware{Transport: client.Transport}
					ctx := context.WithValue(context.Background(), tn.CtxJKExtend{}, &p)
					req, requestErr := http.NewRequestWithContext(ctx, "GET", p.Url, nil)
					if requestErr != nil {
						t.Fatal(requestErr)
					}
					var res *http.Response
					res, err = client.Do(req)
					if res != nil {
						_ = res.Body.Close()
					}
				} else {
					_, _, err = Api(client, p)
				}
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
}
