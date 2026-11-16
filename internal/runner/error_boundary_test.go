package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/dhlanshan/requests/logging"
	"github.com/dhlanshan/requests/tn"
)

type boundaryWriter struct {
	ctx   context.Context
	cause error
}

func (w *boundaryWriter) Write([]byte) (int, error) {
	<-w.ctx.Done()
	return 0, w.cause
}

type boundaryReadFunc func([]byte) (int, error)

func (f boundaryReadFunc) Read(p []byte) (int, error) { return f(p) }

// 等待当前 Context 的期限，不依靠固定 sleep 制造错误与超时的相遇。
// 覆盖执行层、验证器和底层读取，防止标记在到达重试判断前就已经丢失。
func TestForbiddenErrorsSurviveSingleTimeout(t *testing.T) {
	for _, kind := range []string{"writer", "validation", "transport", "buffered_read", "streamed_read", "buffered_limit", "streamed_limit", "error_response_limit", "body_open"} {
		t.Run(kind, func(t *testing.T) {
			cause := errors.New("fixture permanent failure")
			writer := &boundaryWriter{cause: cause}
			var summary logging.CallResult
			var attempts []logging.AttemptResult
			calls, decisions, retryEvents := 0, 0, 0
			p := tn.ApiParam{Url: "http://example.test", Method: "GET", Retry: 1, RetryInterval: time.Nanosecond,
				SingleTimeout: 50 * time.Millisecond, Timeout: 2 * time.Second,
				RetryDecider: func(context.Context, tn.RetryInfo) bool { decisions++; return true },
				Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
					return logging.SessionFuncs{
						AttemptEnd: func(_ context.Context, r logging.AttemptResult) { attempts = append(attempts, r) },
						Retry:      func(context.Context, logging.RetryEvent) { retryEvents++ },
						End:        func(_ context.Context, r logging.CallResult) { summary = r },
					}
				})}}
			wantMarker, wantPhase, wantCalls := tn.ErrPermanent, logging.PhaseResponseRead, 1
			switch kind {
			case "writer":
				wantMarker, wantPhase = tn.ErrResponseWrite, logging.PhaseResponseWrite
			case "validation":
				wantPhase = logging.PhaseValidation
				p.EnableValid, p.RetryValidation = true, true
				p.Validator = func(ctx context.Context, _ []byte, _ http.Header) (bool, error) {
					<-ctx.Done()
					return false, tn.NoRetry(cause)
				}
			case "transport":
				wantMarker, wantPhase = tn.ErrTargetBlocked, logging.PhaseSend
			case "buffered_limit", "streamed_limit", "error_response_limit":
				wantMarker, cause, p.MaxResponseBytes = tn.ErrResponseTooLarge, tn.ErrResponseTooLarge, 1
			case "body_open":
				wantPhase, wantCalls = logging.PhaseBodyOpen, 0
				p.BodyFactory = func(ctx context.Context) (io.ReadCloser, int64, error) {
					<-ctx.Done()
					return nil, 0, tn.NoRetry(cause)
				}
			}
			client := &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				writer.ctx = req.Context()
				if kind == "transport" {
					<-req.Context().Done()
					return nil, errors.Join(tn.ErrTargetBlocked, cause)
				}
				res := logResponse(200, "ok")
				if kind == "error_response_limit" {
					res.StatusCode = http.StatusServiceUnavailable
				}
				if kind == "buffered_read" || kind == "streamed_read" {
					res.ContentLength = -1
					res.Body = io.NopCloser(boundaryReadFunc(func([]byte) (int, error) {
						<-req.Context().Done()
						return 0, tn.NoRetry(cause)
					}))
				} else if kind == "buffered_limit" || kind == "streamed_limit" || kind == "error_response_limit" {
					reads := 0
					res.ContentLength = -1
					res.Body = io.NopCloser(boundaryReadFunc(func(data []byte) (int, error) {
						reads++
						if kind == "streamed_limit" && reads == 1 {
							return copy(data, "a"), nil
						}
						<-req.Context().Done()
						return copy(data, "bb"), nil
					}))
				}
				return res, nil
			})}
			var err error
			if kind == "writer" {
				_, _, err = ExecuteTo(context.Background(), client, p, writer)
			} else if kind == "streamed_read" || kind == "streamed_limit" {
				_, _, err = ExecuteTo(context.Background(), client, p, io.Discard)
			} else {
				_, _, err = Execute(context.Background(), client, p)
			}
			if calls != wantCalls || decisions != 0 || retryEvents != 0 || len(attempts) != 1 || summary.AttemptCount != 1 || summary.RetryCount != 0 {
				t.Fatalf("calls=%d decisions=%d retries=%d attempts=%d summary=%+v", calls, decisions, retryEvents, len(attempts), summary)
			}
			for _, observed := range []error{err, attempts[0].Err, summary.Err} {
				for _, marker := range []error{cause, wantMarker, context.DeadlineExceeded} {
					if !errors.Is(observed, marker) {
						t.Errorf("error lost %v: %v", marker, observed)
					}
				}
				if kind == "validation" && !errors.Is(observed, tn.ErrValidation) {
					t.Errorf("validation marker lost: %v", observed)
				}
			}
			if attempts[0].Phase != wantPhase || summary.Phase != wantPhase {
				t.Fatalf("attempt phase=%s summary phase=%s want=%s", attempts[0].Phase, summary.Phase, wantPhase)
			}
		})
	}
}

func TestReplayLimitKeepsTotalTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var summary logging.CallResult
	p := tn.ApiParam{Url: "http://example.test", Method: "GET", Retry: 1, MaxReplayBytes: 1,
		Body: io.NopCloser(boundaryReadFunc(func(data []byte) (int, error) {
			<-ctx.Done()
			return copy(data, "xx"), nil
		})),
		Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
			return logging.SessionFuncs{End: func(_ context.Context, r logging.CallResult) { summary = r }}
		})}}
	_, _, err := Execute(ctx, &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("oversized replay body was sent")
		return logResponse(200, "ok"), nil
	})}, p)
	for _, observed := range []error{err, summary.Err} {
		if !errors.Is(observed, tn.ErrRequestBodyTooLarge) || !errors.Is(observed, context.DeadlineExceeded) {
			t.Errorf("lost replay limit or timeout: %v", observed)
		}
	}
	if summary.AttemptCount != 0 || summary.Phase != logging.PhaseBodyPreparation {
		t.Fatal(summary)
	}
}

func TestCancellationKeepsFailedAttemptCause(t *testing.T) {
	for _, kind := range []string{"upstream", "total_timeout", "attempt_logger"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("fixture permanent validation failure")
			var summary logging.CallResult
			calls := 0
			p := tn.ApiParam{Url: "http://example.test", Method: "GET", Retry: 1, RetryInterval: time.Nanosecond,
				SingleTimeout: time.Second, Timeout: 2 * time.Second, EnableValid: true, RetryValidation: true,
				Validator: func(ctx context.Context, _ []byte, _ http.Header) (bool, error) {
					if kind == "upstream" {
						cancel()
					} else if kind == "total_timeout" {
						<-ctx.Done()
					}
					return false, tn.NoRetry(cause)
				},
				Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
					return logging.SessionFuncs{
						AttemptEnd: func(context.Context, logging.AttemptResult) {
							if kind == "attempt_logger" {
								cancel()
							}
						},
						End: func(_ context.Context, r logging.CallResult) { summary = r },
					}
				})}}
			wantContext := context.Canceled
			if kind == "total_timeout" {
				p.Timeout, wantContext = 50*time.Millisecond, context.DeadlineExceeded
			}
			_, _, err := Execute(ctx, &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return logResponse(200, "ok"), nil
			})}, p)
			for _, observed := range []error{err, summary.Err} {
				if !errors.Is(observed, cause) || !errors.Is(observed, tn.ErrPermanent) || !errors.Is(observed, wantContext) {
					t.Errorf("lost failure or cancellation: %v", observed)
				}
			}
			if calls != 1 || summary.AttemptCount != 1 || summary.Phase != logging.PhaseValidation {
				t.Fatalf("calls=%d summary=%+v", calls, summary)
			}
		})
	}
}

func TestPlainSingleTimeoutStillRetries(t *testing.T) {
	calls := 0
	p := tn.ApiParam{Url: "http://example.test", Method: "GET", Retry: 1, RetryInterval: time.Nanosecond,
		SingleTimeout: 50 * time.Millisecond, Timeout: 2 * time.Second}
	data, _, err := Execute(context.Background(), &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		return logResponse(200, "ok"), nil
	})}, p)
	if err != nil || calls != 2 || string(data) != "ok" {
		t.Fatalf("calls=%d data=%q err=%v", calls, data, err)
	}
}

func TestCallStatusKeepsLatestResponse(t *testing.T) {
	openFailure := errors.New("fixture body open failure")
	for _, test := range []struct {
		name         string
		statuses     []int
		want         int
		wantErr      error
		openFailure  bool
		redirectDeny bool
	}{
		{name: "response_then_network_errors", statuses: []int{503, 0, 0}, want: 503, wantErr: io.ErrUnexpectedEOF},
		{name: "response_then_success", statuses: []int{503, 200}, want: 200},
		{name: "no_responses", statuses: []int{0, 0}, wantErr: io.ErrUnexpectedEOF},
		{name: "response_then_open_failure", statuses: []int{503, 0}, want: 503, wantErr: openFailure, openFailure: true},
		{name: "redirect_error_with_response", statuses: []int{503, 302}, want: 302, wantErr: tn.ErrTargetBlocked, redirectDeny: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var summary logging.CallResult
			var decisions, retryStatuses []int
			var attempts []logging.AttemptResult
			calls, opened := 0, 0
			p := tn.ApiParam{Url: "http://example.test", Method: "GET", Retry: len(test.statuses) - 1, RetryInterval: time.Nanosecond,
				RetryDecider: func(_ context.Context, info tn.RetryInfo) bool {
					decisions = append(decisions, info.StatusCode)
					return info.DefaultRetry
				},
				Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
					return logging.SessionFuncs{
						AttemptEnd: func(_ context.Context, r logging.AttemptResult) { attempts = append(attempts, r) },
						Retry:      func(_ context.Context, r logging.RetryEvent) { retryStatuses = append(retryStatuses, r.StatusCode) },
						End:        func(_ context.Context, r logging.CallResult) { summary = r },
					}
				})}}
			if test.openFailure {
				p.BodyFactory = func(context.Context) (io.ReadCloser, int64, error) {
					opened++
					if opened > 1 {
						return nil, 0, openFailure
					}
					return http.NoBody, 0, nil
				}
			}
			_, _, err := Execute(context.Background(), &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
				status := test.statuses[calls]
				calls++
				if status == 0 {
					return nil, io.ErrUnexpectedEOF
				}
				res := logResponse(status, "body")
				if test.redirectDeny && status == 302 {
					res.Header.Set("Location", "http://foreign.test/final")
				}
				return res, nil
			})}, p)
			if (test.wantErr == nil && err != nil) || (test.wantErr != nil && !errors.Is(err, test.wantErr)) {
				t.Fatalf("unexpected error: %v", err)
			}
			wantRetries := test.statuses[:len(test.statuses)-1]
			wantCalls := len(test.statuses)
			if test.openFailure {
				wantCalls--
			}
			if summary.StatusCode != test.want || summary.AttemptCount != len(test.statuses) || len(attempts) != len(test.statuses) || calls != wantCalls || !slices.Equal(decisions, wantRetries) || !slices.Equal(retryStatuses, wantRetries) {
				t.Fatalf("summary=%+v decisions=%v retry statuses=%v want=%v", summary, decisions, retryStatuses, wantRetries)
			}
			for i, attempt := range attempts {
				if test.statuses[i] == 0 && attempt.Response != nil {
					t.Errorf("attempt %d invented a response: %+v", i+1, attempt.Response)
				}
			}
		})
	}
}
