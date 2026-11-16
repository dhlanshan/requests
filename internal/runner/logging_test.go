package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhlanshan/requests/logging"
	"github.com/dhlanshan/requests/tn"
)

type logTransportFunc func(*http.Request) (*http.Response, error)

func (f logTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func logResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"COOKIE-SECRET"}}, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
}

func TestLogsAreBoundedAndRedacted(t *testing.T) {
	var output bytes.Buffer
	p := tn.ApiParam{Url: "http://example.test/PATH-SECRET?token=QUERY-SECRET", Method: "POST",
		Body:   strings.NewReader("{\"password\":\"BODY-SECRET\",\"account\":\"CUSTOM-SECRET\"}"),
		Header: map[string]string{"Authorization": "HEADER-SECRET", "X-Custom": "UNKNOWN-SECRET", "Content-Type": "application/json"},
		Logging: &logging.Config{Logger: logging.NewSlogLogger(slog.New(slog.NewJSONHandler(&output, nil))),
			Capture:  logging.CaptureOptions{RequestHeaders: true, ResponseHeaders: true, RequestBody: true, ResponseBody: true},
			Redactor: logging.DefaultRedactor{SensitiveFields: []string{"account"}}}}
	client := &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, req.Body)
		return logResponse(200, "{\"token\":\"RESPONSE-SECRET\",\"ok\":true}"), nil
	})}
	_, _, err := Execute(context.Background(), client, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PATH-SECRET", "QUERY-SECRET", "HEADER-SECRET", "UNKNOWN-SECRET", "BODY-SECRET", "CUSTOM-SECRET", "RESPONSE-SECRET", "COOKIE-SECRET"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("secret leaked: %s", secret)
		}
	}
	if strings.Count(output.String(), "\n") != 3 || !strings.Contains(output.String(), "[REDACTED]") {
		t.Fatal(output.String())
	}
}

// 使用自定义会话检验：它取代内置日志，每次尝试有自己的正文，错误不会被后续成功覆盖。
func TestCustomLoggingCapturesEveryAttempt(t *testing.T) {
	var order []string
	var info logging.CallInfo
	var attempts []logging.AttemptResult
	var summary logging.CallResult
	p := tn.ApiParam{Url: "http://example.test", Method: "GET", Retry: 1, RetryInterval: time.Nanosecond,
		Header: map[string]string{"Content-Type": "application/json"}, TraceId: "trace-1", Caller: "orders"}
	p.Logging = &logging.Config{Redactor: logging.RedactorFuncs{},
		Capture: logging.CaptureOptions{RequestHeaders: true, ResponseHeaders: true, RequestBody: true, ResponseBody: true},
		Logger: logging.LoggerFunc(func(_ context.Context, c logging.CallInfo) logging.Session {
			info = c
			order = append(order, "start")
			return logging.SessionFuncs{
				AttemptEnd: func(_ context.Context, r logging.AttemptResult) {
					order = append(order, "attempt")
					attempts = append(attempts, r)
				},
				Retry: func(_ context.Context, r logging.RetryEvent) {
					order = append(order, "retry")
					if r.Attempt != 1 || r.NextAttempt != 2 || r.Err == nil || r.StatusCode != 503 {
						t.Errorf("bad retry: %#v", r)
					}
				},
				End: func(_ context.Context, r logging.CallResult) { order = append(order, "end"); summary = r },
			}
		})}
	opened, sent := 0, 0
	p.BodyFactory = func(context.Context) (io.ReadCloser, int64, error) {
		opened++
		payload := fmt.Sprintf("{\"attempt\":%d}", opened)
		return io.NopCloser(strings.NewReader(payload)), int64(len(payload)), nil
	}
	client := &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
		sent++
		_, _ = io.Copy(io.Discard, req.Body)
		if sent == 1 {
			return logResponse(503, "{\"error\":\"temporary\"}"), nil
		}
		return logResponse(200, "{\"ok\":true}"), nil
	})}
	data, _, err := Execute(context.Background(), client, p)
	if err != nil || string(data) != "{\"ok\":true}" {
		t.Fatalf("data=%s err=%v", data, err)
	}
	if !slices.Equal(order, []string{"start", "attempt", "retry", "attempt", "end"}) {
		t.Fatal(order)
	}
	if info.RequestID == "" || info.TraceID != "trace-1" || info.Caller != "orders" || opened != 2 {
		t.Fatal(info, opened)
	}
	if len(attempts) != 2 || attempts[0].Err == nil || attempts[1].Err != nil || attempts[0].Phase != logging.PhaseStatus {
		t.Fatal(attempts)
	}
	for i, r := range attempts {
		if string(r.Request.Body.Data) != fmt.Sprintf("{\"attempt\":%d}", i+1) || !r.Request.Body.Complete || !r.Response.Body.Complete {
			t.Fatalf("attempt snapshot: %#v", r)
		}
	}
	if string(attempts[0].Response.Body.Data) != "{\"error\":\"temporary\"}" || summary.AttemptCount != 2 || summary.RetryCount != 1 || summary.Err != nil || summary.Duration <= 0 {
		t.Fatal(summary, attempts)
	}
}

func TestLoggingEndsOnEarlyReturns(t *testing.T) {
	for _, scenario := range []string{"nil_context", "bad_url", "cancelled", "policy", "body_open", "body_prepare"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			var end logging.CallResult
			ended, events := 0, 0
			limiter, _ := tn.NewLimiter(1, 0)
			p := tn.ApiParam{Url: "http://example.test", Method: "GET",
				Limiter: limiter,
				Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
					return logging.SessionFuncs{AttemptEnd: func(context.Context, logging.AttemptResult) { events++ }, End: func(_ context.Context, r logging.CallResult) { ended++; end = r }}
				})}}
			wantPhase, wantAttempts := logging.PhaseParameters, 0
			switch scenario {
			case "nil_context":
				ctx = nil
			case "bad_url":
				p.Url = "invalid"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "policy":
				wantPhase = logging.PhasePolicy
				p.URLPolicy = func(context.Context, *url.URL) error { return errors.New("blocked") }
			case "body_open":
				wantPhase, wantAttempts = logging.PhaseBodyOpen, 1
				p.BodyFactory = func(context.Context) (io.ReadCloser, int64, error) { return nil, 0, errors.New("open failed") }
			case "body_prepare":
				wantPhase = logging.PhaseBodyPreparation
				p.Retry = 1
				p.Body = &errorLogReader{}
			}
			client := &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
				t.Error("unexpected send")
				return logResponse(200, "{}"), nil
			})}
			_, _, err := Execute(ctx, client, p)
			if err == nil || ended != 1 || end.Err == nil || end.Phase != wantPhase || end.AttemptCount != wantAttempts || events != wantAttempts {
				t.Fatalf("err=%v ended=%d events=%d end=%#v", err, ended, events, end)
			}
			// 包括 Body 准备、打开失败在内的提前返回，都必须归还已经取得的名额。
			checkCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			release, acquireErr := limiter.Acquire(checkCtx, 0)
			if acquireErr != nil {
				t.Fatal("early return leaked the limiter slot", acquireErr)
			}
			release()
		})
	}
}

type errorLogReader struct{}

func (*errorLogReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestRetryPlanCancelledDoesNotCountAsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var summary logging.CallResult
	p := tn.ApiParam{Url: "http://example.test", Method: "GET", Retry: 2,
		Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
			return logging.SessionFuncs{Retry: func(context.Context, logging.RetryEvent) { cancel() }, End: func(_ context.Context, r logging.CallResult) { summary = r }}
		})}}
	_, _, err := Execute(ctx, &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) { return logResponse(503, "{}"), nil })}, p)
	if !errors.Is(err, context.Canceled) || summary.AttemptCount != 1 || summary.RetryCount != 0 || summary.Phase != logging.PhaseRetryWait {
		t.Fatal(err, summary)
	}
}

func TestSnapshotMutationDoesNotChangeHTTPData(t *testing.T) {
	p := tn.ApiParam{Url: "http://example.test", Method: "GET", Header: map[string]string{"X-Test": "original"},
		Logging: &logging.Config{Redactor: logging.RedactorFuncs{}, Capture: logging.CaptureOptions{RequestHeaders: true, ResponseHeaders: true, ResponseBody: true},
			Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
				return logging.SessionFuncs{AttemptEnd: func(_ context.Context, r logging.AttemptResult) {
					r.Request.Header.Set("X-Test", "mutated")
					r.Response.Header.Set("X-Test", "mutated")
					r.Response.Body.Data[0] = 'X'
				}}
			})}}
	client := &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("X-Test") != "original" {
			t.Error("request changed")
		}
		r := logResponse(200, "{\"ok\":true}")
		r.Header.Set("X-Test", "original")
		return r, nil
	})}
	body, header, err := Execute(context.Background(), client, p)
	if err != nil || string(body) != "{\"ok\":true}" || header.Get("X-Test") != "original" {
		t.Fatal(string(body), header, err)
	}
}

func TestStreamingLoggingIsBounded(t *testing.T) {
	var snapshot logging.BodySnapshot
	var summary logging.CallResult
	p := tn.ApiParam{Url: "http://example.test", Method: "GET",
		Logging: &logging.Config{Redactor: logging.RedactorFuncs{}, Capture: logging.CaptureOptions{ResponseBody: true, ResponseBodyLimit: 4},
			Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
				return logging.SessionFuncs{AttemptEnd: func(_ context.Context, r logging.AttemptResult) { snapshot = r.Response.Body }, End: func(_ context.Context, r logging.CallResult) { summary = r }}
			})}}
	var dst bytes.Buffer
	n, _, err := ExecuteTo(context.Background(), &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) { return logResponse(200, "abcdefgh"), nil })}, p, &dst)
	if err != nil || n != 8 || dst.String() != "abcdefgh" || string(snapshot.Data) != "abcd" || !snapshot.Truncated || !snapshot.Complete || summary.Err != nil {
		t.Fatal(n, err, snapshot, summary)
	}
}

func TestDisabledLoggerSkipsCapture(t *testing.T) {
	var output bytes.Buffer
	p := tn.ApiParam{Url: "http://example.test", Method: "POST", Body: strings.NewReader("{}"),
		Logging: &logging.Config{Logger: logging.NewSlogLogger(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn}))), Capture: logging.CaptureOptions{RequestBody: true}}}
	_, _, err := Execute(context.Background(), &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
		if _, ok := req.Body.(*captureBody); ok {
			t.Error("capture allocated for disabled logger")
		}
		_, _ = io.Copy(io.Discard, req.Body)
		return logResponse(200, "{}"), nil
	})}, p)
	if err != nil || output.Len() != 0 {
		t.Fatal(err, output.String())
	}
}

func TestLoggingSessionsAreIndependent(t *testing.T) {
	var mu sync.Mutex
	ids := map[string]bool{}
	p := tn.ApiParam{Url: "http://example.test", Method: "GET", Logging: &logging.Config{Logger: logging.LoggerFunc(func(_ context.Context, info logging.CallInfo) logging.Session {
		count := 0
		return logging.SessionFuncs{AttemptEnd: func(context.Context, logging.AttemptResult) { count++ }, End: func(context.Context, logging.CallResult) {
			mu.Lock()
			defer mu.Unlock()
			if count != 1 || ids[info.RequestID] {
				t.Errorf("shared session or ID: count=%d id=%s", count, info.RequestID)
			}
			ids[info.RequestID] = true
		}}
	})}}
	client := &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) { return logResponse(200, "{}"), nil })}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := Execute(context.Background(), client, p); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(ids) != 20 {
		t.Fatal(len(ids))
	}
}

// 日志扩展各阶段即使 panic，也不能把一次原本能重试成功的调用变为失败。
func TestLoggingPanicDoesNotChangeRetries(t *testing.T) {
	reported, sent := 0, 0
	p := tn.ApiParam{Url: "http://example.test", Method: "GET", Retry: 1, RetryInterval: time.Nanosecond,
		Logging: &logging.Config{
			OnError: func(context.Context, error) {
				reported++
				panic("reporter failed")
			},
			Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
				return logging.SessionFuncs{
					AttemptEnd: func(context.Context, logging.AttemptResult) { panic("attempt logger failed") },
					Retry:      func(context.Context, logging.RetryEvent) { panic("retry logger failed") },
					End:        func(context.Context, logging.CallResult) { panic("end logger failed") },
				}
			}),
		}}
	client := &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
		sent++
		if sent == 1 {
			return logResponse(503, "{}"), nil
		}
		return logResponse(200, "{\"ok\":true}"), nil
	})}
	body, _, err := Execute(context.Background(), client, p)
	if err != nil || string(body) != "{\"ok\":true}" || sent != 2 || reported != 4 {
		t.Fatal(string(body), err, sent, reported)
	}
}

type cleanupLogBody struct {
	io.Reader
	closed atomic.Int32
}

func (b *cleanupLogBody) Close() error { b.closed.Add(1); return nil }

// OnEnd 可安全发起另一次调用：流与共享配额都应在最终回调之前释放。
func TestLoggingEndFollowsResourceCleanup(t *testing.T) {
	limiter, _ := tn.NewLimiter(1, 64<<10)
	upload := &cleanupLogBody{Reader: strings.NewReader("{}")}
	download := &cleanupLogBody{Reader: strings.NewReader("{}")}
	ended := false
	p := tn.ApiParam{Url: "http://example.test", Method: "POST", Body: upload, Limiter: limiter, MaxResponseBytes: 32,
		Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
			return logging.SessionFuncs{End: func(_ context.Context, result logging.CallResult) {
				ended = true
				if upload.closed.Load() != 1 || download.closed.Load() != 1 || result.Err != nil {
					t.Error("OnEnd preceded body cleanup", upload.closed.Load(), download.closed.Load(), result)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				release, err := limiter.Acquire(ctx, 32)
				if err != nil {
					t.Error("OnEnd preceded limiter release", err)
					return
				}
				release()
			}}
		})}}
	client := &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, req.Body)
		resp := logResponse(200, "{}")
		resp.Body = download
		return resp, nil
	})}
	if _, _, err := Execute(context.Background(), client, p); err != nil || !ended {
		t.Fatal(err, ended)
	}
}

func TestLoggingCaptureReservesAdmissionBudget(t *testing.T) {
	limiter, _ := tn.NewLimiter(1, 8)
	var summary logging.CallResult
	p := tn.ApiParam{Url: "http://example.test", Method: "GET", MaxResponseBytes: 8, Limiter: limiter,
		Logging: &logging.Config{
			Capture: logging.CaptureOptions{ResponseBody: true, ResponseBodyLimit: 1},
			Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
				return logging.SessionFuncs{End: func(_ context.Context, r logging.CallResult) { summary = r }}
			}),
		}}
	client := &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("request exceeded admission budget")
		return logResponse(200, "{}"), nil
	})}
	_, _, err := Execute(context.Background(), client, p)
	if !errors.Is(err, tn.ErrAdmissionTooLarge) || summary.Phase != logging.PhaseAdmission || summary.AttemptCount != 0 {
		t.Fatal(err, summary)
	}
}

func TestLoggingPreservesIncompleteResponse(t *testing.T) {
	var attempt logging.AttemptResult
	p := tn.ApiParam{Url: "http://example.test", Method: "GET",
		Logging: &logging.Config{Capture: logging.CaptureOptions{ResponseBody: true}, Redactor: logging.RedactorFuncs{},
			Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
				return logging.SessionFuncs{AttemptEnd: func(_ context.Context, r logging.AttemptResult) { attempt = r }}
			})}}
	client := &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
		resp := logResponse(200, "")
		resp.ContentLength = -1
		resp.Body = io.NopCloser(io.MultiReader(strings.NewReader("abc"), &errorLogReader{}))
		return resp, nil
	})}
	body, _, err := Execute(context.Background(), client, p)
	if err == nil || string(body) != "abc" || attempt.Phase != logging.PhaseResponseRead || attempt.Response == nil {
		t.Fatal(string(body), err, attempt)
	}
	snapshot := attempt.Response.Body
	if snapshot.Complete || snapshot.Truncated || string(snapshot.Data) != "abc" {
		t.Fatal(snapshot)
	}
}

func TestStreamingLoggingEndsOnSetupError(t *testing.T) {
	for _, scenario := range []string{"nil_context", "nil_writer", "validation"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			var dst io.Writer = io.Discard
			if scenario == "nil_context" {
				ctx = nil
			}
			if scenario == "nil_writer" {
				dst = nil
			}
			upload := &cleanupLogBody{Reader: strings.NewReader("{}")}
			ended := 0
			p := tn.ApiParam{Url: "http://example.test", Method: "GET", Body: upload, EnableValid: scenario == "validation",
				Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
					return logging.SessionFuncs{End: func(_ context.Context, result logging.CallResult) {
						ended++
						if result.AttemptCount != 0 || result.Err == nil || result.Phase != logging.PhaseParameters || upload.closed.Load() != 1 {
							t.Error(result, upload.closed.Load())
						}
					}}
				})}}
			_, _, err := ExecuteTo(ctx, nil, p, dst)
			if err == nil || ended != 1 {
				t.Fatal(err, ended)
			}
		})
	}
}

func TestStartLoggingConsumesTotalTimeout(t *testing.T) {
	var summary logging.CallResult
	p := tn.ApiParam{Url: "http://example.test", Method: "GET", Timeout: time.Millisecond,
		Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
			time.Sleep(5 * time.Millisecond)
			return logging.SessionFuncs{End: func(_ context.Context, r logging.CallResult) { summary = r }}
		})}}
	client := &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("start logger exhausted total timeout")
		return logResponse(200, "{}"), nil
	})}
	_, _, err := Execute(context.Background(), client, p)
	if !errors.Is(err, context.DeadlineExceeded) || summary.AttemptCount != 0 || summary.Duration < 5*time.Millisecond {
		t.Fatal(err, summary)
	}
}

func TestAttemptLoggingCancellationDoesNotReportComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var summary logging.CallResult
	p := tn.ApiParam{Url: "http://example.test", Method: "GET",
		Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
			return logging.SessionFuncs{
				AttemptEnd: func(context.Context, logging.AttemptResult) { cancel() },
				End:        func(_ context.Context, r logging.CallResult) { summary = r },
			}
		})}}
	_, _, err := Execute(ctx, &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
		return logResponse(200, "{}"), nil
	})}, p)
	if !errors.Is(err, context.Canceled) || summary.Phase != logging.PhaseAttempt || !errors.Is(summary.Err, context.Canceled) {
		t.Fatal(err, summary)
	}
}

func TestRetryDelayIsCapped(t *testing.T) {
	p := tn.ApiParam{RetryInterval: time.Second, MaxRetryDelay: 2 * time.Second}
	for _, value := range []string{"999999999999", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)} {
		if delay := retryDelay(p, 1, http.Header{"Retry-After": {value}}); delay != 2*time.Second {
			t.Fatalf("delay=%s", delay)
		}
	}
	p.Backoff = func(int) time.Duration { return -time.Second }
	if delay := retryDelay(p, 1, nil); delay != 0 {
		t.Fatalf("negative delay=%s", delay)
	}
}

func TestDefaultTimeoutDoesNotOverflow(t *testing.T) {
	p := tn.ApiParam{SingleTimeout: time.Duration(1<<63 - 1), Retry: 100}
	defaults(&p)
	if p.Timeout != time.Duration(1<<63-1) {
		t.Fatalf("overflowed timeout: %s", p.Timeout)
	}
}
