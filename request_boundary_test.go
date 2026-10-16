package requests

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/dhlanshan/requests/tn"
)

func TestClientConstructorsRejectTypedNilJar(t *testing.T) {
	var jar *cookiejar.Jar
	for name, constructor := range map[string]func(tn.ClientParam) (*http.Client, error){
		"named": NewClientChecked, "uncached": NewClientUncached, "public": NewPublicClient,
	} {
		t.Run(name, func(t *testing.T) {
			client, err := constructor(tn.ClientParam{SpaceName: t.Name(), Jar: jar})
			if !errors.Is(err, tn.ErrClientConfig) || client != nil {
				t.Fatalf("nil jar accepted: client=%p err=%v", client, err)
			}
		})
	}
	client := NewClient(tn.ClientParam{SpaceName: t.Name(), Jar: jar})
	if _, _, err := Api(client, params()); !errors.Is(err, tn.ErrClientConfig) {
		t.Fatalf("legacy constructor did not fail closed: %v", err)
	}
}

type boundaryBodySource struct {
	open tn.BodyFactory
}

func (s boundaryBodySource) Read([]byte) (int, error) { return 0, io.EOF }
func (s boundaryBodySource) Replayable() bool         { return true }
func (s boundaryBodySource) NewBody(ctx context.Context) (io.ReadCloser, int64, error) {
	return s.open(ctx)
}

func TestDeclaredZeroLengthChecksBodyBeforeSending(t *testing.T) {
	for _, kind := range []string{"reader", "factory_unknown", "factory_zero", "source_unknown"} {
		for _, payload := range []string{"", "unexpected"} {
			t.Run(kind+"/"+payload, func(t *testing.T) {
				var opened []*trackingBody
				factory := func(context.Context) (io.ReadCloser, int64, error) {
					body := &trackingBody{Reader: strings.NewReader(payload)}
					opened = append(opened, body)
					length := int64(-1)
					if kind == "factory_zero" {
						length = 0
					}
					return body, length, nil
				}
				p := params()
				p.Method, p.Header = "POST", map[string]string{"Content-Length": "0"}
				switch kind {
				case "reader":
					p.Body, _, _ = factory(context.Background())
				case "source_unknown":
					p.Body = boundaryBodySource{open: factory}
				default:
					p.BodyFactory = factory
				}
				calls := 0
				client := testClient(func(req *http.Request) (*http.Response, error) {
					calls++
					var wire bytes.Buffer
					if err := req.Write(&wire); err != nil {
						return nil, err
					}
					parsed, err := http.ReadRequest(bufio.NewReader(&wire))
					if err != nil {
						return nil, err
					}
					defer parsed.Body.Close()
					data, err := io.ReadAll(parsed.Body)
					if err != nil || len(data) != 0 || len(parsed.TransferEncoding) != 0 || parsed.Header.Get("Content-Length") != "0" {
						t.Errorf("body=%q transfer=%v header=%v err=%v", data, parsed.TransferEncoding, parsed.Header, err)
					}
					return response(200, "ok"), nil
				})
				_, _, err := Api(client, p)
				if payload == "" {
					if err != nil || calls != 1 {
						t.Fatalf("empty body rejected: calls=%d err=%v", calls, err)
					}
				} else if err == nil || calls != 0 {
					t.Fatalf("nonempty body sent: calls=%d err=%v", calls, err)
				}
				for _, body := range opened {
					if body.closes.Load() != 1 {
						t.Fatalf("body close count=%d", body.closes.Load())
					}
				}
			})
		}
	}
}

func TestDeclaredZeroLengthProbeHonorsCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	p := params()
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Length": "0"}
	_, _, err := ApiContext(ctx, testClient(func(*http.Request) (*http.Response, error) {
		t.Error("sent body before verifying explicit zero length")
		return response(200, "ok"), nil
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestDeclaredZeroLengthRejectsBeforeReplayBuffering(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = writer.Write([]byte("x"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p := params()
	p.Method, p.Body, p.Header = "POST", reader, map[string]string{"Content-Length": "0"}
	p.Retry, p.AllowUnsafeRetry = 1, true
	_, _, err := ApiContext(ctx, testClient(func(*http.Request) (*http.Response, error) {
		t.Error("sent nonempty body with declared zero length")
		return response(200, "ok"), nil
	}), p)
	<-finished
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waited for replay buffering instead of rejecting the first byte: %v", err)
	}
}

func TestDeclaredZeroLengthReplaysWithoutBufferBudget(t *testing.T) {
	limiter, err := tn.NewLimiter(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	body := &trackingBody{Reader: strings.NewReader("")}
	p := params()
	p.Method, p.Body, p.Header = "POST", body, map[string]string{"Content-Length": "0"}
	p.Retry, p.AllowUnsafeRetry, p.MaxResponseBytes, p.Limiter = 1, true, 1, limiter
	calls := 0
	_, _, err = Api(testClient(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Body != http.NoBody {
			t.Error("verified empty body was not normalized")
		}
		if calls == 1 {
			return response(503, ""), nil
		}
		return response(200, ""), nil
	}), p)
	if err != nil || calls != 2 || body.closes.Load() != 1 {
		t.Fatalf("calls=%d closes=%d err=%v", calls, body.closes.Load(), err)
	}
}
