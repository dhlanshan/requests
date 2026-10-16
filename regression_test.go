package requests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhlanshan/requests/interfaces"
	"github.com/dhlanshan/requests/mdw"
	"github.com/dhlanshan/requests/tn"
)

type isolatedMiddleware struct{ next http.RoundTripper }

func (m *isolatedMiddleware) Name() string                        { return "isolated" }
func (m *isolatedMiddleware) SetTransport(next http.RoundTripper) { m.next = next }
func (m *isolatedMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.next.RoundTrip(req)
}

type clonedMiddleware struct{ isolatedMiddleware }

func (m *clonedMiddleware) Clone() interfaces.Middleware { return &clonedMiddleware{} }

func TestMiddlewareIsolationRejectsSharedMutableNodes(t *testing.T) {
	shared := &isolatedMiddleware{}
	if _, err := NewClientUncached(tn.ClientParam{Middlewares: []interfaces.Middleware{shared}}); !errors.Is(err, tn.ErrClientConfig) {
		t.Fatalf("uncloneable node accepted: %v", err)
	}
	prototype := &clonedMiddleware{}
	protected, err := NewPublicClient(tn.ClientParam{NetworkPolicy: &tn.NetworkPolicy{AllowedHosts: []string{"allowed.example"}}, Middlewares: []interfaces.Middleware{prototype}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = NewClientUncached(tn.ClientParam{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return response(200, "unsafe"), nil }), Middlewares: []interfaces.Middleware{prototype}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Api(protected, tn.ApiParam{Url: "http://127.0.0.1:1/private", Method: "GET"})
	if !errors.Is(err, tn.ErrTargetBlocked) || calls != 0 {
		t.Fatalf("policy bypass: calls=%d err=%v", calls, err)
	}
	if prototype.next != nil {
		t.Fatal("prototype was mutated")
	}
}

func TestRedirectBoundariesAndExplicitBodyPermission(t *testing.T) {
	for _, tc := range []struct {
		name, destination               string
		cross, downgrade, body, allowed bool
	}{
		{"cross_origin_denied", "https://other.test/next", false, false, false, false},
		{"downgrade_denied", "http://other.test/next", true, false, true, false},
		{"body_denied", "https://other.test/next", true, false, false, false},
		{"explicit_body", "https://other.test/next", true, false, true, true},
		{"same_origin_default_port", "https://SOURCE.test:443/next", false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := testClient(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					resp := response(307, "")
					resp.Header.Set("Location", tc.destination)
					return resp, nil
				}
				payload, err := io.ReadAll(req.Body)
				if err != nil || string(payload) != "private-body" {
					t.Errorf("body=%q err=%v", payload, err)
				}
				if tc.cross && req.Header.Get("X-Access") != "" {
					t.Error("unknown credential header leaked")
				}
				return response(200, "ok"), nil
			})
			p := params()
			p.Url = "https://source.test/start"
			p.Method = "POST"
			p.Body = strings.NewReader("private-body")
			p.Header = map[string]string{"X-Access": "credential"}
			p.AllowCrossOriginRedirect, p.AllowHTTPSDowngrade, p.AllowCrossOriginBody = tc.cross, tc.downgrade, tc.body
			_, _, err := Api(client, p)
			if tc.allowed {
				if err != nil || calls != 2 {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			} else if !errors.Is(err, tn.ErrTargetBlocked) || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestCrossOriginCookieJarDoesNotReintroduceCredentials(t *testing.T) {
	var received string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Cookie")
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(source.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "private", Path: "/"}})
	client := source.Client()
	client.Jar = jar
	p := params()
	p.Url = source.URL
	p.AllowCrossOriginRedirect = true
	if _, _, err := Api(client, p); err != nil {
		t.Fatal(err)
	}
	if received != "" {
		t.Fatal("CookieJar reintroduced cross-origin credentials")
	}
}

func TestJSONEmptyRetryListStaysDisabled(t *testing.T) {
	for _, codes := range [][]int{nil, {}, {503}} {
		p := params()
		p.Retry = 1
		p.RetryStatusCodes = codes
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var restored tn.ApiParam
		if err = json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		if (codes == nil) != (restored.RetryStatusCodes == nil) {
			t.Fatal("nil/empty distinction lost")
		}
		calls := 0
		_, _, err = Api(testClient(func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return response(503, "retry"), nil
			}
			return response(200, "ok"), nil
		}), restored)
		want := 2
		if codes != nil && len(codes) == 0 {
			want = 1
		}
		if calls != want {
			t.Fatalf("codes=%v calls=%d err=%v", codes, calls, err)
		}
	}
}

func TestInvalidQueryRejectedWithoutSending(t *testing.T) {
	for _, query := range []string{"bad=%zz", "bad=%", "bad=x;y"} {
		p := params()
		p.Url += "?good=1&" + query
		p.Params = map[string]string{"extra": "2"}
		_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) { t.Fatal("invalid query sent"); return nil, nil }), p)
		if err == nil {
			t.Fatal("invalid query accepted")
		}
	}
}

func TestValidationRetryClassification(t *testing.T) {
	permanent := errors.New("permanent business failure")
	for _, tc := range []struct {
		name                 string
		enable, mark, decide bool
		want                 int
	}{
		{"default_no_validation_retry", false, false, false, 1},
		{"explicit_temporary", true, false, false, 3},
		{"permanent_veto", true, true, true, 1},
		{"custom_decider", false, false, true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := params()
			p.Retry = 2
			p.EnableValid = true
			p.RetryValidation = tc.enable
			p.Validator = func([]byte, http.Header) (bool, error) {
				if tc.mark {
					return false, tn.NoRetry(permanent)
				}
				return false, permanent
			}
			if tc.decide {
				p.RetryDecider = func(_ context.Context, info tn.RetryInfo) bool {
					if info.Attempt < 1 {
						t.Error("invalid attempt")
					}
					return true
				}
			}
			calls := 0
			_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) { calls++; return response(200, "business"), nil }), p)
			if calls != tc.want || !errors.Is(err, permanent) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestNetworkErrorDisplayOmitsURLAndUnwraps(t *testing.T) {
	cause := errors.New("fixture network failure")
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) { return nil, cause }), tn.ApiParam{Url: "https://example.test/private?token=fixture-secret", Method: "GET"})
	var requestErr *tn.RequestError
	var urlErr *url.Error
	if !errors.As(err, &requestErr) || !errors.As(err, &urlErr) || !errors.Is(err, cause) || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unsafe or uninspectable error: %v", err)
	}
}

func TestNamedClientConflictAndExplicitLookup(t *testing.T) {
	name := t.Name()
	first, err := NewClientChecked(tn.ClientParam{SpaceName: name, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewClientChecked(tn.ClientParam{SpaceName: name, Timeout: 2 * time.Second}); !errors.Is(err, tn.ErrClientConfig) {
		t.Fatal("timeout conflict silently reused")
	}
	if got, ok := GetClient(name); !ok || got != first {
		t.Fatal("explicit lookup failed")
	}
	second, err := NewClientChecked(tn.ClientParam{SpaceName: name, Timeout: 2 * time.Second, Reset: true})
	if err != nil || second == first {
		t.Fatal("reset failed", err)
	}
	fn := func(*http.Request, []*http.Request) error { return nil }
	_, err = NewClientChecked(tn.ClientParam{SpaceName: name + "-callback", CheckRedirect: fn})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewClientChecked(tn.ClientParam{SpaceName: name + "-callback", CheckRedirect: fn}); !errors.Is(err, tn.ErrClientConfig) {
		t.Fatal("opaque callback identity assumed equal")
	}
	for i := 0; i < 2; i++ {
		client, err := NewClientUncached(tn.ClientParam{CheckRedirect: fn})
		if err != nil {
			t.Fatal(err)
		}
		client.CloseIdleConnections()
	}
}

func TestStreamingOutputLimitsAndNoRetryAfterWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		limit   int64
		readErr error
		want    error
	}{
		{"exact_limit", "1234", 4, nil, nil},
		{"too_large", "12345", 4, nil, tn.ErrResponseTooLarge},
		{"partial_read_error", "123", 20, io.ErrUnexpectedEOF, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := testClient(func(*http.Request) (*http.Response, error) {
				calls++
				resp := response(200, tc.payload)
				if tc.readErr != nil {
					resp.Body = io.NopCloser(&failingReader{data: tc.payload, err: tc.readErr})
				}
				return resp, nil
			})
			p := params()
			p.Retry = 2
			p.MaxResponseBytes = tc.limit
			p.RetryDecider = func(context.Context, tn.RetryInfo) bool { return true }
			var dst bytes.Buffer
			n, _, err := ApiTo(context.Background(), client, p, &dst)
			if !errors.Is(err, tc.want) || n != int64(dst.Len()) || n > tc.limit || calls != 1 {
				t.Fatalf("n=%d calls=%d err=%v", n, calls, err)
			}
		})
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestStreamingRetriesStatusBeforeWritingAndStopsOnWriterFailure(t *testing.T) {
	p := params()
	p.Retry = 2
	calls := 0
	client := testClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(503, "unavailable"), nil
		}
		return response(200, "ok"), nil
	})
	var dst bytes.Buffer
	n, _, err := ApiTo(context.Background(), client, p, &dst)
	if err != nil || calls != 2 || n != 2 || dst.String() != "ok" {
		t.Fatal(n, calls, err)
	}
	cause := errors.New("disk full")
	calls = 0
	p.RetryDecider = func(context.Context, tn.RetryInfo) bool { return true }
	_, _, err = ApiTo(context.Background(), testClient(func(*http.Request) (*http.Response, error) { calls++; return response(200, "ok"), nil }), p, failingWriter{cause})
	if !errors.Is(err, tn.ErrResponseWrite) || !errors.Is(err, cause) || calls != 1 {
		t.Fatal(calls, err)
	}
}

func TestAdmissionPrecedesBodyPreparationAndHonorsCancellation(t *testing.T) {
	limiter, _ := tn.NewLimiter(1, 32<<20)
	release, err := limiter.Acquire(context.Background(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var opened atomic.Int32
	p := params()
	p.Limiter = limiter
	p.BodyFactory = func(context.Context) (io.ReadCloser, int64, error) { opened.Add(1); return http.NoBody, 0, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err = ApiContext(ctx, testClient(func(*http.Request) (*http.Response, error) { t.Fatal("sent while queued"); return nil, nil }), p)
	if !errors.Is(err, context.DeadlineExceeded) || opened.Load() != 0 {
		t.Fatal(opened.Load(), err)
	}
	tooSmall, _ := tn.NewLimiter(1, 1)
	p.Limiter = tooSmall
	if _, _, err = Api(nil, p); !errors.Is(err, tn.ErrAdmissionTooLarge) {
		t.Fatal(err)
	}
}

func TestAdmissionBoundsConcurrentCalls(t *testing.T) {
	limiter, _ := tn.NewLimiter(2, 0)
	var active, peak atomic.Int32
	gate := make(chan struct{})
	entered := make(chan struct{}, 2)
	client := testClient(func(*http.Request) (*http.Response, error) {
		n := active.Add(1)
		for previous := peak.Load(); n > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, n) {
				break
			}
		}
		entered <- struct{}{}
		<-gate
		active.Add(-1)
		return response(200, "ok"), nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := params()
			p.Limiter = limiter
			if _, _, err := Api(client, p); err != nil {
				t.Error(err)
			}
		}()
	}
	<-entered
	<-entered
	close(gate)
	// 后续调用的通知也需要消费，避免阻塞测试的模拟 Transport。
	go func() {
		for i := 0; i < 4; i++ {
			<-entered
		}
	}()
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("peak=%d", peak.Load())
	}
}

func TestLegacyMiddlewaresRetryAndLogWithoutDuplicateExecution(t *testing.T) {
	var output bytes.Buffer
	meta := params()
	meta.Retry = 1
	meta.EchoReq = true
	meta.EchoRes = true
	meta.Logger = slog.New(slog.NewJSONHandler(&output, nil))
	bus := &tn.InternalBus{RequestId: "legacy-id"}
	ctx := context.WithValue(context.Background(), tn.CtxJKExtend{}, &meta)
	ctx = context.WithValue(ctx, tn.CtxBSExtend{}, bus)
	calls := 0
	client, err := NewClientUncached(tn.ClientParam{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if len(req.Header.Values("X-Multi")) != 2 {
			t.Error("multi-value header lost")
		}
		if calls == 1 {
			return response(503, "again"), nil
		}
		return response(200, "ok"), nil
	}), Middlewares: []interfaces.Middleware{&mdw.LoggingMiddleware{}, &mdw.RetryMiddleware{}}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", meta.Url, nil)
	req.Header["X-Multi"] = []string{"one", "two"}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(data) != "ok" || calls != 2 || strings.Count(output.String(), "\n") != 2 {
		t.Fatalf("calls=%d body=%q log=%s err=%v", calls, data, output.String(), err)
	}
	stats, ok := bus.MdwDataMap.Load("Retry Middleware")
	if !ok || stats.(*tn.RetryData).TryCnt != 1 {
		t.Fatal("legacy bus lost")
	}
}

func TestHTTP2AndTLSVerification(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Error("HTTP/2 not negotiated")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	p := params()
	p.Url = server.URL
	if body, _, err := Api(server.Client(), p); err != nil || string(body) != "ok" {
		t.Fatal(string(body), err)
	}
	if _, _, err := Api(nil, p); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
}
