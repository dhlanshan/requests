package runner

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhlanshan/requests/logging"
	"github.com/dhlanshan/requests/tn"
)

// 全部调用同时停在 Transport 内，才能证明默认路径没有原先 64 个调用/256 MiB 的门槛。
// 不访问真实网络，也不依赖每次请求的响应速度来推测并发数量。
func TestNilLimiterAllowsConcurrentCalls(t *testing.T) {
	const calls = 128
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gate := make(chan struct{})
	entered := make(chan struct{}, calls)
	results := make(chan error, calls)
	var wg sync.WaitGroup
	var gateOnce sync.Once
	releaseGate := func() { gateOnce.Do(func() { close(gate) }) }
	defer func() { releaseGate(); wg.Wait() }()

	p := tn.ApiParam{Method: http.MethodGet, Logging: &logging.Config{
		Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
			return logging.SessionFuncs{End: func(_ context.Context, result logging.CallResult) {
				if result.LimiterWaitDuration != 0 || result.AttemptCount != 1 || result.Err != nil {
					t.Error("unexpected unconfigured-limiter result", result)
				}
			}}
		}),
	}}
	newClient := func() *http.Client {
		return &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
			entered <- struct{}{}
			select {
			case <-gate:
				return logResponse(200, "{}"), nil
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		})}
	}
	clients := []*http.Client{newClient(), newClient()}
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			param := p
			param.Url = []string{"https://a.example.test", "https://b.example.test"}[i%2]
			_, _, err := Execute(ctx, clients[i%2], param)
			results <- err
		}()
	}
	for count := range calls {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatalf("only %d/%d calls reached Transport: %v", count, calls, ctx.Err())
		}
	}
	releaseGate()
	wg.Wait()
	for range calls {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}

// 正文上限相加会溢出，但实际正文很小。未启用容量限制时不应计算或拒绝该预算。
func TestBufferBudgetIsExplicit(t *testing.T) {
	for _, mode := range []string{"nil", "concurrency_only", "with_capacity"} {
		t.Run(mode, func(t *testing.T) {
			var end logging.CallResult
			p := tn.ApiParam{Url: "https://example.test", Method: http.MethodGet,
				Body: strings.NewReader("{}"), MaxResponseBytes: math.MaxInt64 - 1,
				Logging: &logging.Config{
					Capture: logging.CaptureOptions{
						RequestBody: true, ResponseBody: true,
						RequestBodyLimit: math.MaxInt64 / 2, ResponseBodyLimit: math.MaxInt64 / 2,
					},
					Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
						return logging.SessionFuncs{End: func(_ context.Context, r logging.CallResult) { end = r }}
					}),
				}}
			switch mode {
			case "concurrency_only":
				p.Limiter, _ = tn.NewLimiter(1, 0)
			case "with_capacity":
				p.Limiter, _ = tn.NewLimiter(1, 32<<20)
			}
			sent := 0
			client := &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
				sent++
				_, _ = io.Copy(io.Discard, req.Body)
				return logResponse(200, "{}"), nil
			})}
			body, _, err := Execute(context.Background(), client, p)
			if mode == "with_capacity" {
				if !errors.Is(err, tn.ErrAdmissionTooLarge) || sent != 0 || end.Phase != logging.PhaseAdmission || end.LimiterWaitDuration != 0 {
					t.Fatal(err, sent, end)
				}
				return
			}
			if err != nil || string(body) != "{}" || sent != 1 || end.AttemptCount != 1 {
				t.Fatal(string(body), err, sent, end)
			}
			if mode == "nil" && end.LimiterWaitDuration != 0 {
				t.Fatal(end)
			}
			if mode == "concurrency_only" && end.LimiterWaitDuration <= 0 {
				t.Fatal(end)
			}
		})
	}
}

func TestLimiterScopesAndCancelledWait(t *testing.T) {
	shared, _ := tn.NewLimiter(1, 0)
	other, _ := tn.NewLimiter(1, 0)
	held, err := shared.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held()

	for _, scenario := range []struct {
		name    string
		limiter *tn.Limiter
	}{{"shared_a", shared}, {"shared_b", shared}, {"independent", other}, {"disabled", nil}} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ready := make(chan struct{})
			done := make(chan error, 1)
			var sent, opened atomic.Int32
			var end logging.CallResult
			p := tn.ApiParam{Url: "https://" + strings.ReplaceAll(scenario.name, "_", "-") + ".example.test", Method: http.MethodGet, Limiter: scenario.limiter,
				BodyFactory: func(context.Context) (io.ReadCloser, int64, error) {
					opened.Add(1)
					return http.NoBody, 0, nil
				},
				URLPolicy: func(context.Context, *url.URL) error { close(ready); return nil },
				Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
					return logging.SessionFuncs{End: func(_ context.Context, r logging.CallResult) { end = r }}
				})}}
			client := &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
				sent.Add(1)
				return logResponse(200, "{}"), nil
			})}
			go func() {
				_, _, err := Execute(ctx, client, p)
				done <- err
			}()
			// 参数检查已经完成后再取消，避免把入口取消误当作等待名额时的取消。
			select {
			case <-ready:
			case err := <-done:
				t.Fatal("call returned before the initial URL policy", err)
			case <-ctx.Done():
				<-done
				t.Fatal("call did not reach the initial URL policy", ctx.Err())
			}
			if scenario.limiter == shared {
				cancel()
			}
			select {
			case err := <-done:
				if scenario.limiter == shared {
					if !errors.Is(err, context.Canceled) || sent.Load() != 0 || opened.Load() != 0 ||
						end.Phase != logging.PhaseAdmission || end.AttemptCount != 0 || end.RetryCount != 0 ||
						end.LimiterWaitDuration <= 0 || end.LimiterWaitDuration > end.Duration {
						t.Fatal(err, sent.Load(), opened.Load(), end)
					}
				} else if err != nil || sent.Load() != 1 || end.AttemptCount != 1 {
					t.Fatal(err, sent.Load(), end)
				}
				if scenario.limiter == nil && end.LimiterWaitDuration != 0 {
					t.Fatal(end)
				}
			case <-time.After(2 * time.Second):
				cancel()
				<-done
				t.Fatal("another limiter's occupied slot blocked this call")
			}
		})
	}
}

// 重试事件发生时名额应仍被当前调用持有；OnEnd 时应已经归还。
func TestLimiterSlotSpansRetriesAndIsReleasedBeforeEnd(t *testing.T) {
	limiter, _ := tn.NewLimiter(1, 0)
	retried, ended, sent := 0, 0, 0
	p := tn.ApiParam{Url: "https://example.test", Method: http.MethodGet, Retry: 1, RetryInterval: time.Nanosecond, Timeout: 2 * time.Second, Limiter: limiter,
		Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
			return logging.SessionFuncs{
				Retry: func(context.Context, logging.RetryEvent) {
					retried++
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
					defer cancel()
					release, err := limiter.Acquire(ctx, 0)
					if err == nil {
						release()
						t.Error("retry released the call's slot")
					} else if !errors.Is(err, context.DeadlineExceeded) {
						t.Error(err)
					}
				},
				End: func(_ context.Context, result logging.CallResult) {
					ended++
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					release, err := limiter.Acquire(ctx, 0)
					if err != nil {
						t.Error("OnEnd preceded slot release", err)
						return
					}
					release()
					if result.AttemptCount != 2 || result.RetryCount != 1 || result.Err != nil || result.LimiterWaitDuration <= 0 {
						t.Error(result)
					}
				},
			}
		})}}
	client := &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
		sent++
		if sent == 1 {
			return logResponse(503, "{}"), nil
		}
		return logResponse(200, "{}"), nil
	})}
	if _, _, err := Execute(context.Background(), client, p); err != nil || sent != 2 || retried != 1 || ended != 1 {
		t.Fatal(err, sent, retried, ended)
	}
}

func TestInvalidLimiterFailsBeforeSending(t *testing.T) {
	upload := &cleanupLogBody{Reader: strings.NewReader("{}")}
	var end logging.CallResult
	p := tn.ApiParam{Url: "https://example.test", Method: http.MethodGet, Body: upload, Limiter: &tn.Limiter{},
		Logging: &logging.Config{Logger: logging.LoggerFunc(func(context.Context, logging.CallInfo) logging.Session {
			return logging.SessionFuncs{End: func(_ context.Context, result logging.CallResult) { end = result }}
		})}}
	client := &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("invalid limiter sent a request")
		return logResponse(200, "{}"), nil
	})}
	_, _, err := Execute(context.Background(), client, p)
	if err == nil || upload.closed.Load() != 1 || end.Phase != logging.PhaseParameters || end.AttemptCount != 0 || end.LimiterWaitDuration != 0 {
		t.Fatal(err, upload.closed.Load(), end)
	}
}
