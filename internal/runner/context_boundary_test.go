package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dhlanshan/requests/logging"
	"github.com/dhlanshan/requests/tn"
)

// 在扩展内部确定取消/期限已经发生，验证所有早退方向都保留 Context 原因。
func TestCallbackExitsKeepContextCause(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		for _, stage := range []string{"decider_false", "decider_true", "policy_error", "policy_nil", "source_error", "source_nil"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cause := errors.New("fixture secret failure")
				var summary logging.CallResult
				calls, retries, opens := 0, 0, 0
				var opened *canonicalBody
				p := tn.ApiParam{Url: "http://example.test", Method: "GET", Retry: 1, RetryInterval: time.Nanosecond,
					SingleTimeout: time.Second, Timeout: time.Second,
					Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
						return logging.SessionFuncs{
							Retry: func(context.Context, logging.RetryEvent) { retries++ },
							End:   func(_ context.Context, result logging.CallResult) { summary = result },
						}
					})}}
				wantContext := context.Canceled
				if mode == "deadline" {
					p.Timeout, wantContext = 25*time.Millisecond, context.DeadlineExceeded
				}
				stop := func(current context.Context) {
					if mode == "cancel" {
						cancel()
					} else {
						<-current.Done()
					}
				}
				wantCalls, wantPhase := 0, logging.PhasePolicy
				switch {
				case strings.HasPrefix(stage, "decider"):
					wantCalls, wantPhase = 1, logging.PhaseStatus
					p.RetryDecider = func(current context.Context, _ tn.RetryInfo) bool {
						stop(current)
						return stage == "decider_true"
					}
				case strings.HasPrefix(stage, "policy"):
					p.URLPolicy = func(current context.Context, _ *url.URL) error {
						stop(current)
						if stage == "policy_error" {
							return cause
						}
						return nil
					}
				default:
					wantPhase = logging.PhaseBodyPreparation
					p.Body = canonicalSource{open: func(current context.Context) (io.ReadCloser, int64, error) {
						opens++
						stop(current)
						opened = &canonicalBody{Reader: strings.NewReader("unused")}
						if stage == "source_error" {
							return opened, -1, cause
						}
						return opened, -1, nil
					}}
				}
				_, _, err := Execute(ctx, &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return logResponse(503, ""), nil
				})}, p)
				for _, observed := range []error{err, summary.Err} {
					if !errors.Is(observed, wantContext) {
						t.Errorf("missing context cause: %v", observed)
					}
					if strings.HasSuffix(stage, "error") && !errors.Is(observed, cause) {
						t.Errorf("original cause lost: %v", observed)
					}
					if stage == "policy_error" {
						var safe *tn.RequestError
						if !errors.Is(observed, tn.ErrTargetBlocked) || !errors.As(observed, &safe) || strings.Contains(observed.Error(), "secret") {
							t.Errorf("policy wrapping changed: %v", observed)
						}
					}
					if strings.HasPrefix(stage, "decider") {
						var status *tn.HTTPError
						if !errors.As(observed, &status) || status.StatusCode != 503 {
							t.Errorf("failed attempt lost: %v", observed)
						}
					}
				}
				if calls != wantCalls || retries != 0 || summary.Phase != wantPhase || summary.AttemptCount != wantCalls {
					t.Fatalf("calls=%d retries=%d summary=%+v", calls, retries, summary)
				}
				if opened != nil && (opened.closes.Load() != 1 || opens != 1) {
					t.Fatalf("source opens=%d closes=%d", opens, opened.closes.Load())
				}
			})
		}
	}
}

func TestBodyOpenLoggerCancellationKeepsOpenError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cause := errors.New("fixture open failure")
	p := tn.ApiParam{Url: "http://example.test", Method: "GET",
		BodyFactory: func(context.Context) (io.ReadCloser, int64, error) { return nil, 0, cause },
		Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
			return logging.SessionFuncs{AttemptEnd: func(context.Context, logging.AttemptResult) { cancel() }}
		})}}
	_, _, err := Execute(ctx, &http.Client{}, p)
	if !errors.Is(err, cause) || !errors.Is(err, context.Canceled) {
		t.Fatalf("open/logging error lost: %v", err)
	}
}
