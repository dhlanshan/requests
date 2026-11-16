package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dhlanshan/requests/logging"
	"github.com/dhlanshan/requests/tn"
)

type panicWriter struct{ stop func() }

func (w panicWriter) Write([]byte) (int, error) { w.stop(); return 0, nil }

// 同时检查异常传播、两个层级的日志、关闭次数、Context 和名额释放顺序。
// Goexit 只能在独立 goroutine 中验证；done 保证读取记录时不存在并发访问。
func TestAbnormalExecutionLogsAndCleansUp(t *testing.T) {
	for _, mode := range []string{"panic", "panic_nil", "goexit"} {
		for _, stage := range []string{"policy", "body_prepare", "body_open", "empty_probe", "transport", "status", "response_read", "request_close", "response_close", "writer", "invalid_writer", "decider", "backoff", "redirect_policy", "redirect_callback"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				secret := &struct{ value string }{"PANIC-SECRET"}
				stop := func() {
					switch mode {
					case "goexit":
						runtime.Goexit()
					case "panic_nil":
						panic(nil)
					default:
						panic(secret)
					}
				}
				limiter, _ := tn.NewLimiter(1, 0)
				var summary logging.CallResult
				var attempts []logging.AttemptResult
				var order []string
				var streams []*canonicalBody
				var current context.Context
				ended, calls := 0, 0
				closedBeforeEnd, canceledBeforeAttemptEnd, releasedBeforeEnd := true, true, false
				p := tn.ApiParam{Url: "http://example.test", Method: "GET", Retry: 1, RetryInterval: time.Nanosecond, Limiter: limiter,
					Logging: &logging.Config{Capture: logging.CaptureOptions{RequestBody: true, ResponseBody: true},
						Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
							return logging.SessionFuncs{
								AttemptEnd: func(_ context.Context, result logging.AttemptResult) {
									order = append(order, "attempt")
									attempts = append(attempts, result)
									if current != nil && current.Err() == nil {
										canceledBeforeAttemptEnd = false
									}
								},
								End: func(_ context.Context, result logging.CallResult) {
									order = append(order, "end")
									ended++
									summary = result
									for _, stream := range streams {
										if stream.closes.Load() != 1 {
											closedBeforeEnd = false
										}
									}
									checkCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
									defer cancel()
									release, err := limiter.Acquire(checkCtx, 0)
									if err == nil {
										releasedBeforeEnd = true
										release()
									}
								},
							}
						})}}
				newStream := func(reader io.Reader) *canonicalBody {
					body := &canonicalBody{Reader: reader}
					streams = append(streams, body)
					return body
				}
				p.BodyFactory = func(ctx context.Context) (io.ReadCloser, int64, error) {
					current = ctx
					if stage == "body_open" {
						stop()
					}
					if stage == "empty_probe" {
						return newStream(boundaryReadFunc(func([]byte) (int, error) { stop(); return 0, io.EOF })), 0, nil
					}
					body := newStream(strings.NewReader("x"))
					if stage == "request_close" {
						body.onClose = stop
					}
					return body, 1, nil
				}
				wantAttempts := 1
				wantAbortedAttempt := true
				switch stage {
				case "invalid_writer":
					wantAttempts = 0
					p.BodyFactory = nil
					body := newStream(strings.NewReader("x"))
					body.onClose, p.Body = stop, body
				case "policy":
					wantAttempts = 0
					p.BodyFactory, p.Body = nil, newStream(strings.NewReader("x"))
					p.URLPolicy = func(context.Context, *url.URL) error { stop(); return nil }
				case "body_prepare":
					wantAttempts = 0
					p.BodyFactory = nil
					p.Body = canonicalSource{open: func(ctx context.Context) (io.ReadCloser, int64, error) { current = ctx; stop(); return nil, 0, nil }}
				case "status":
					p.SuccessStatus = func(int) bool { stop(); return false }
				case "decider":
					wantAbortedAttempt = false
					p.RetryDecider = func(context.Context, tn.RetryInfo) bool { stop(); return true }
				case "backoff":
					wantAbortedAttempt = false
					p.Backoff = func(int) time.Duration { stop(); return 0 }
				case "redirect_policy":
					p.URLPolicy = func(_ context.Context, u *url.URL) error {
						if u.Path == "/next" {
							stop()
						}
						return nil
					}
				}
				client := &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					current = req.Context()
					if stage == "transport" {
						stop()
					}
					resp := logResponse(200, "ok")
					var reader io.Reader = strings.NewReader("ok")
					if stage == "response_read" {
						reader = boundaryReadFunc(func([]byte) (int, error) { stop(); return 0, io.EOF })
					}
					respBody := newStream(reader)
					if stage == "response_close" {
						respBody.onClose = stop
					}
					resp.Body = respBody
					switch stage {
					case "decider", "backoff":
						resp.StatusCode = 503
					case "redirect_policy", "redirect_callback":
						resp.StatusCode = 307
						resp.Header.Set("Location", "/next")
					}
					return resp, nil
				})}
				if stage == "redirect_callback" {
					client.CheckRedirect = func(*http.Request, []*http.Request) error { stop(); return nil }
				}
				var recovered any
				returned := false
				done := make(chan struct{})
				go func() {
					defer close(done)
					defer func() { recovered = recover() }()
					if stage == "invalid_writer" {
						_, _, _ = ExecuteTo(context.Background(), client, p, nil)
					} else if stage == "writer" {
						_, _, _ = ExecuteTo(context.Background(), client, p, panicWriter{stop: stop})
					} else {
						_, _, _ = Execute(context.Background(), client, p)
					}
					returned = true
				}()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("abnormal execution did not terminate")
				}
				if returned || (mode == "panic" && recovered != secret) || (mode == "goexit" && recovered != nil) {
					t.Fatalf("panic propagation changed: returned=%t recovered=%T", returned, recovered)
				}
				if ended != 1 || summary.Phase != logging.PhaseAborted || !errors.Is(summary.Err, tn.ErrExecutionAborted) || summary.AttemptCount != wantAttempts || len(attempts) != wantAttempts {
					t.Fatalf("ended=%d attempts=%d summary=%+v", ended, len(attempts), summary)
				}
				if strings.Contains(summary.Err.Error(), "PANIC-SECRET") || !closedBeforeEnd || !releasedBeforeEnd || !canceledBeforeAttemptEnd {
					t.Fatalf("unsafe end or cleanup: closed=%t released=%t canceled=%t", closedBeforeEnd, releasedBeforeEnd, canceledBeforeAttemptEnd)
				}
				if current != nil && current.Err() == nil {
					t.Fatal("Context remains active after abnormal exit")
				}
				if wantAttempts > 0 {
					if wantAbortedAttempt && (attempts[0].Phase != logging.PhaseAborted || !errors.Is(attempts[0].Err, tn.ErrExecutionAborted)) {
						t.Fatalf("attempt not aborted: %+v", attempts[0])
					}
					if !wantAbortedAttempt && (attempts[0].Phase != logging.PhaseStatus || summary.StatusCode != 503) {
						t.Fatalf("completed attempt overwritten: %+v", attempts[0])
					}
					if len(order) != 2 || order[0] != "attempt" || order[1] != "end" {
						t.Fatal("wrong event order", order)
					}
				}
				if calls > 1 {
					t.Fatal("abnormal exit was retried")
				}
			})
		}
	}
}
