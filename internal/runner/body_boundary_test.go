package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhlanshan/requests/tn"
)

type canonicalBody struct {
	io.Reader
	closes  atomic.Int32
	onClose func()
}

func (b *canonicalBody) Close() error {
	b.closes.Add(1)
	if b.onClose != nil {
		b.onClose()
	}
	return nil
}

type canonicalSource struct {
	open       tn.BodyFactory
	replayable bool
}

func (s canonicalSource) Read([]byte) (int, error) { return 0, io.EOF }
func (s canonicalSource) Replayable() bool         { return s.replayable }
func (s canonicalSource) NewBody(ctx context.Context) (io.ReadCloser, int64, error) {
	return s.open(ctx)
}

// 不仅检查内存中的字段，还序列化 HTTP/1.1 请求，防止空 Reader 再次变成 chunked。
func TestKnownEmptyBodiesPreserveWireSemantics(t *testing.T) {
	for _, kind := range []string{"factory", "source", "one_shot_source"} {
		t.Run(kind, func(t *testing.T) {
			var opened []*canonicalBody
			factory := func(context.Context) (io.ReadCloser, int64, error) {
				body := &canonicalBody{Reader: strings.NewReader("")}
				opened = append(opened, body)
				return body, 0, nil
			}
			p := tn.ApiParam{Url: "http://example.test", Method: "POST", Retry: 1, AllowUnsafeRetry: true, RetryInterval: time.Nanosecond}
			if kind == "factory" {
				p.BodyFactory = factory
			} else {
				p.Body = canonicalSource{open: factory, replayable: kind == "source"}
			}
			calls := 0
			_, _, err := Execute(context.Background(), &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Body != http.NoBody || req.ContentLength != 0 || req.GetBody == nil {
					t.Fatalf("noncanonical empty request: body=%T length=%d", req.Body, req.ContentLength)
				}
				reopened, err := req.GetBody()
				if err != nil || reopened != http.NoBody {
					t.Fatalf("noncanonical replay: body=%T err=%v", reopened, err)
				}
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
					t.Fatalf("body=%q transfer=%v header=%v err=%v", data, parsed.TransferEncoding, parsed.Header, err)
				}
				if calls == 1 {
					return logResponse(503, ""), nil
				}
				return logResponse(200, ""), nil
			})}, p)
			if err != nil || calls != 2 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			for _, body := range opened {
				if body.closes.Load() != 1 {
					t.Fatalf("empty body closed %d times", body.closes.Load())
				}
			}
		})
	}
}

func TestZeroLengthFactoryRejectsDataAndChangedReplay(t *testing.T) {
	for _, onReplay := range []bool{false, true} {
		t.Run(map[bool]string{false: "first_open", true: "get_body"}[onReplay], func(t *testing.T) {
			var opened []*canonicalBody
			plan := bodyPlan{replayable: true, open: func(context.Context) (io.ReadCloser, int64, error) {
				payload := "unexpected"
				if onReplay && len(opened) == 0 {
					payload = ""
				}
				body := &canonicalBody{Reader: strings.NewReader(payload)}
				opened = append(opened, body)
				return body, 0, nil
			}}
			template, _ := http.NewRequest("POST", "http://example.test", nil)
			req, err := plan.request(context.Background(), template)
			if onReplay {
				if err != nil {
					t.Fatal(err)
				}
				defer req.Body.Close()
				_, err = req.GetBody()
			}
			if err == nil {
				t.Fatal("nonempty zero-length body accepted")
			}
			for _, body := range opened {
				if body.closes.Load() != 1 {
					t.Fatalf("body closed %d times", body.closes.Load())
				}
			}
		})
	}
}

func TestUnknownReaderStillUploads(t *testing.T) {
	for _, retry := range []int{0, 1} {
		t.Run(map[int]string{0: "stream", 1: "buffered_replay"}[retry], func(t *testing.T) {
			body := &canonicalBody{Reader: strings.NewReader("stream payload")}
			calls := 0
			_, _, err := Execute(context.Background(), &http.Client{Transport: logTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				data, err := io.ReadAll(req.Body)
				if err != nil || string(data) != "stream payload" {
					t.Fatalf("unknown stream lost: %q %v", data, err)
				}
				if retry > 0 && calls == 1 {
					return logResponse(503, ""), nil
				}
				return logResponse(200, ""), nil
			})}, tn.ApiParam{Url: "http://example.test", Method: "POST", Body: body, Retry: retry, AllowUnsafeRetry: true, RetryInterval: time.Nanosecond})
			if err != nil || calls != retry+1 || body.closes.Load() != 1 {
				t.Fatalf("calls=%d closes=%d err=%v", calls, body.closes.Load(), err)
			}
		})
	}
}

func TestNoBodyLengthConsistency(t *testing.T) {
	for _, length := range []int64{-2, -1, 0, 1} {
		plan := bodyPlan{open: func(context.Context) (io.ReadCloser, int64, error) { return http.NoBody, length, nil }, replayable: true}
		template, _ := http.NewRequest("POST", "http://example.test", nil)
		req, err := plan.request(context.Background(), template)
		if length < -1 || length > 0 {
			if err == nil {
				t.Fatalf("NoBody accepted length=%d", length)
			}
			continue
		}
		if err != nil || req.Body != http.NoBody || req.ContentLength != 0 {
			t.Fatalf("length=%d req=%v err=%v", length, req, err)
		}
		r, err := req.GetBody()
		if err != nil || r != http.NoBody {
			t.Fatalf("NoBody replay: %T %v", r, err)
		}
	}
}

func TestImplicitZeroLengthProbeHonorsCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	p := tn.ApiParam{Url: "http://example.test", Method: "POST", BodyFactory: func(context.Context) (io.ReadCloser, int64, error) { return reader, 0, nil }}
	_, _, err := Execute(ctx, &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("unverified body sent")
		return logResponse(200, ""), nil
	})}, p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing timeout: %v", err)
	}
}

func TestEmptyBodyCloseCancellationStopsSending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &canonicalBody{Reader: strings.NewReader(""), onClose: cancel}
	p := tn.ApiParam{Url: "http://example.test", Method: "POST", BodyFactory: func(context.Context) (io.ReadCloser, int64, error) { return body, 0, nil }}
	_, _, err := Execute(ctx, &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("sent after empty-body cleanup cancelled the context")
		return logResponse(200, ""), nil
	})}, p)
	if !errors.Is(err, context.Canceled) || body.closes.Load() != 1 {
		t.Fatalf("err=%v closes=%d", err, body.closes.Load())
	}
}
