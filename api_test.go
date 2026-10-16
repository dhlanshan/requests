package requests

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhlanshan/requests/pf"
	"github.com/dhlanshan/requests/tc"
	"github.com/dhlanshan/requests/tn"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func testClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			defer r.Body.Close()
		}
		return fn(r)
	})}
}
func params() tn.ApiParam {
	return tn.ApiParam{Url: "http://example.test/path", Method: http.MethodGet, RetryInterval: time.Nanosecond}
}

func TestRequestAndSuccessfulStatuses(t *testing.T) {
	for _, status := range []int{200, 201, 202, 204, 206} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p := params()
			p.Url += "?key=old"
			p.Params = map[string]string{"key": "new", "unicode": "你好"}
			p.Header = map[string]string{"Host": "virtual.test", "X-Test": "yes"}
			client := testClient(func(req *http.Request) (*http.Response, error) {
				if req.Host != "virtual.test" || req.Header.Get("X-Test") != "yes" || len(req.URL.Query()["key"]) != 2 || req.URL.Query().Get("unicode") != "你好" {
					t.Fatal("request metadata was lost")
				}
				if _, ok := pf.GetRequestMeta(req.Context()); !ok {
					t.Fatal("missing request metadata")
				}
				if _, ok := pf.GetBusMeta(req.Context()); !ok {
					t.Fatal("missing request bus")
				}
				return response(status, "ok"), nil
			})
			body, header, err := Api(client, p)
			if err != nil || string(body) != "ok" || header.Get("Content-Type") == "" {
				t.Fatalf("body=%q header=%v err=%v", body, header, err)
			}
		})
	}
}

func TestHTTPErrorPreservesBodyAndLimit(t *testing.T) {
	p := params()
	p.MaxErrorBytes = 4
	body, header, err := Api(testClient(func(*http.Request) (*http.Response, error) { return response(400, "abcdef"), nil }), p)
	var httpErr *tn.HTTPError
	if !errors.As(err, &httpErr) || !errors.Is(err, tn.ErrResponseTooLarge) || !httpErr.Truncated || httpErr.StatusCode != 400 || string(body) != "abcd" || header.Get("Content-Type") == "" {
		t.Fatalf("body=%q error=%#v", body, err)
	}
	p.MaxResponseBytes = 3
	body, _, err = Api(testClient(func(*http.Request) (*http.Response, error) { return response(200, "abcdef"), nil }), p)
	if !errors.Is(err, tn.ErrResponseTooLarge) || string(body) != "abc" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	p.SuccessStatus = func(code int) bool { return code == 304 }
	_, _, err = Api(testClient(func(*http.Request) (*http.Response, error) { return response(304, ""), nil }), p)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetrySafetyAndStatusPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		unsafe, key  bool
		status, want int
		codes        []int
	}{
		{name: "temporary_get", method: "GET", status: 503, want: 2},
		{name: "rate_limit", method: "GET", status: 429, want: 2},
		{name: "permanent_get", method: "GET", status: 400, want: 1},
		{name: "post_default", method: "POST", status: 503, want: 1},
		{name: "post_opt_in", method: "POST", unsafe: true, status: 503, want: 2},
		{name: "post_idempotency", method: "POST", key: true, status: 503, want: 2},
		{name: "status_retry_disabled", method: "GET", status: 503, want: 1, codes: []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := params()
			p.Method, p.Retry, p.AllowUnsafeRetry = tc.method, 2, tc.unsafe
			p.Body = strings.NewReader("payload")
			p.RetryStatusCodes = tc.codes
			if tc.key {
				p.Header = map[string]string{"Idempotency-Key": "one-operation"}
			}
			calls := 0
			client := testClient(func(req *http.Request) (*http.Response, error) {
				data, err := io.ReadAll(req.Body)
				if err != nil || string(data) != "payload" {
					t.Fatalf("replayed body %q %v", data, err)
				}
				calls++
				if calls == 1 {
					return response(tc.status, "temporary"), nil
				}
				return response(200, "ok"), nil
			})
			_, _, err := Api(client, p)
			if calls != tc.want {
				t.Fatalf("got %d attempts, want %d", calls, tc.want)
			}
			if (err == nil) != (tc.want == 2) {
				t.Fatalf("unexpected result %v", err)
			}
		})
	}
}

type trackingBody struct {
	io.Reader
	closes atomic.Int32
}

func (b *trackingBody) Close() error { b.closes.Add(1); return nil }

type failingReader struct {
	data string
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, r.err
}

func TestReplayErrorsAndBodyOwnership(t *testing.T) {
	failure := errors.New("input failed")
	for _, tc := range []struct {
		name   string
		reader io.Reader
		limit  int64
		want   error
	}{
		{"read_error", &failingReader{data: "part", err: failure}, 10, failure},
		{"too_large", strings.NewReader("123456"), 4, tn.ErrRequestBodyTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackingBody{Reader: tc.reader}
			p := params()
			p.Body, p.Retry, p.MaxReplayBytes = body, 1, tc.limit
			calls := 0
			_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) { calls++; return response(200, ""), nil }), p)
			if !errors.Is(err, tc.want) || calls != 0 || body.closes.Load() != 1 {
				t.Fatalf("err=%v calls=%d closes=%d", err, calls, body.closes.Load())
			}
		})
	}
	body := &trackingBody{Reader: strings.NewReader("unused")}
	p := params()
	p.Url, p.Body = "file:///etc/passwd", body
	_, _, err := Api(nil, p)
	if err == nil || body.closes.Load() != 1 {
		t.Fatalf("invalid request leaked body: %v", err)
	}
}

func TestBodyFactoryStreamsAndReopens(t *testing.T) {
	p := params()
	p.Method, p.Retry, p.AllowUnsafeRetry, p.MaxReplayBytes = "POST", 1, true, 1
	var bodies []*trackingBody
	p.BodyFactory = func(ctx context.Context) (io.ReadCloser, int64, error) {
		body := &trackingBody{Reader: strings.NewReader("large-enough")}
		bodies = append(bodies, body)
		return body, 12, nil
	}
	calls := 0
	_, _, err := Api(testClient(func(req *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(req.Body)
		if err != nil || string(data) != "large-enough" {
			t.Fatalf("body=%q err=%v", data, err)
		}
		calls++
		if calls == 1 {
			return response(503, ""), nil
		}
		return response(200, ""), nil
	}), p)
	if err != nil || calls != 2 || len(bodies) != 2 {
		t.Fatalf("calls=%d factories=%d err=%v", calls, len(bodies), err)
	}
	for _, body := range bodies {
		if body.closes.Load() != 1 {
			t.Fatalf("close count=%d", body.closes.Load())
		}
	}
}

func TestResponseReadFailureIsRetriedAndClosed(t *testing.T) {
	p := params()
	p.Retry = 1
	bad := &trackingBody{Reader: &failingReader{data: "partial", err: io.ErrUnexpectedEOF}}
	calls := 0
	body, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			resp := response(200, "")
			resp.Body = bad
			return resp, nil
		}
		return response(200, "complete"), nil
	}), p)
	if err != nil || calls != 2 || string(body) != "complete" || bad.closes.Load() != 1 {
		t.Fatalf("body=%q calls=%d closes=%d err=%v", body, calls, bad.closes.Load(), err)
	}
}

func TestCancellationAndAttemptTimeout(t *testing.T) {
	p := params()
	p.Retry, p.SingleTimeout, p.Timeout = 1, 20*time.Millisecond, time.Second
	calls := 0
	_, _, err := Api(testClient(func(req *http.Request) (*http.Response, error) {
		calls++
		<-req.Context().Done()
		return nil, req.Context().Err()
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = ApiContext(ctx, testClient(func(*http.Request) (*http.Response, error) { t.Fatal("called after cancel"); return nil, nil }), params())
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCancellationWhilePreparingBody(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	p := params()
	p.Body, p.Retry = reader, 1
	_, _, err := ApiContext(ctx, testClient(func(*http.Request) (*http.Response, error) { t.Fatal("sent unreadable body"); return nil, nil }), p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestCancellationDuringRetryAfter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	p := params()
	p.Retry = 10
	calls := 0
	_, _, err := ApiContext(ctx, testClient(func(*http.Request) (*http.Response, error) {
		calls++
		resp := response(429, "")
		resp.Header.Set("Retry-After", "999999999999")
		return resp, nil
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestValidation(t *testing.T) {
	cause := errors.New("business error")
	for _, tc := range []struct {
		name string
		fn   tn.Validator
		want error
	}{
		{"false_nil", func([]byte, http.Header) (bool, error) { return false, nil }, tn.ErrValidation},
		{"true_error", func([]byte, http.Header) (bool, error) { return true, cause }, cause},
		{"panic", func([]byte, http.Header) (bool, error) { panic("private data") }, tn.ErrValidation},
		{"default", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := params()
			p.EnableValid, p.Validator = true, tc.fn
			_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) { return response(200, `{"code":200,"msg":"success"}`), nil }), p)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	p := params()
	p.SingleTimeout, p.EnableValid = 10*time.Millisecond, true
	p.ContextValidator = func(ctx context.Context, _ []byte, _ http.Header) (bool, error) { <-ctx.Done(); return true, nil }
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) { return response(200, ""), nil }), p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late validation succeeded: %v", err)
	}
}

func TestRedirectBeforeValidationAndReplay(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusTemporaryRedirect)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "payload" {
			t.Errorf("redirect body=%q err=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"msg":"success"}`)
	}))
	defer server.Close()
	p := params()
	p.Url, p.Method, p.Body, p.EnableValid = server.URL+"/start", "POST", strings.NewReader("payload"), true
	_, _, err := Api(server.Client(), p)
	if err != nil || calls.Load() != 2 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestStreamingRequestWithoutRetry(t *testing.T) {
	p := params()
	body := &trackingBody{Reader: strings.NewReader("123456")}
	p.Body, p.MaxReplayBytes = body, 1
	_, _, err := Api(testClient(func(req *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(req.Body)
		if err != nil || string(data) != "123456" {
			t.Fatalf("body=%q err=%v", data, err)
		}
		return response(200, ""), nil
	}), p)
	if err != nil || body.closes.Load() != 1 {
		t.Fatalf("closes=%d err=%v", body.closes.Load(), err)
	}
}

func TestInvalidParametersNeverSend(t *testing.T) {
	for _, mutate := range []func(*tn.ApiParam){
		func(p *tn.ApiParam) { p.Retry = -1 },
		func(p *tn.ApiParam) { p.Retry = 101 },
		func(p *tn.ApiParam) { p.SingleTimeout = -1 },
		func(p *tn.ApiParam) { p.Url = "http://user:secret@example.test" },
		func(p *tn.ApiParam) { p.Header = map[string]string{"Bad Header": "x"} },
		func(p *tn.ApiParam) { p.Header = map[string]string{"X-Test": "x\r\nInjected:yes"} },
		func(p *tn.ApiParam) { p.Header = map[string]string{"x-test": "x", "X-Test": "y"} },
		func(p *tn.ApiParam) {
			p.Body = bytes.NewReader([]byte("abc"))
			p.Header = map[string]string{"Content-Length": "2"}
		},
	} {
		p := params()
		mutate(&p)
		_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) { t.Fatal("invalid request sent"); return nil, nil }), p)
		if err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestCompressedResponseLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = io.WriteString(writer, strings.Repeat("x", 8192))
		_ = writer.Close()
	}))
	defer server.Close()
	p := params()
	p.Url, p.MaxResponseBytes = server.URL, 64
	body, _, err := Api(server.Client(), p)
	if !errors.Is(err, tn.ErrResponseTooLarge) || len(body) != 64 {
		t.Fatalf("size=%d err=%v", len(body), err)
	}
}

func TestMultipartRetryUsesStreamingSource(t *testing.T) {
	reader, contentType, err := tc.ContentByFormData(map[string]any{"file": tc.FormFile{Filename: "file.bin", Content: bytes.Repeat([]byte("x"), 8192)}})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(16384); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		data, err := io.ReadAll(file)
		file.Close()
		if err != nil || len(data) != 8192 {
			t.Errorf("size=%d err=%v", len(data), err)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	p := params()
	p.Url, p.Method, p.Body = server.URL, "POST", reader
	p.Header = map[string]string{"Content-Type": contentType}
	p.Retry, p.AllowUnsafeRetry, p.MaxReplayBytes = 1, true, 1
	_, _, err = Api(server.Client(), p)
	if err != nil || calls.Load() != 2 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestCancellationDuringResponseRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	p := params()
	p.SingleTimeout = 20 * time.Millisecond
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) {
		resp := response(200, "")
		resp.Body = reader
		return resp, nil
	}), p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestBodyFactoryRejectsNilAndLengthMismatch(t *testing.T) {
	for _, factory := range []tn.BodyFactory{
		func(context.Context) (io.ReadCloser, int64, error) { var body *trackingBody; return body, 0, nil },
		func(context.Context) (io.ReadCloser, int64, error) {
			return io.NopCloser(strings.NewReader("123")), 3, nil
		},
	} {
		p := params()
		p.BodyFactory = factory
		p.Header = map[string]string{"Content-Length": "2"}
		_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) { t.Fatal("invalid factory result sent"); return nil, nil }), p)
		if err == nil {
			t.Fatal("invalid factory result accepted")
		}
	}
}

func TestBlankIdempotencyKeyDoesNotEnableRetry(t *testing.T) {
	p := params()
	p.Method = "POST"
	p.Retry = 2
	p.Header = map[string]string{"Idempotency-Key": " "}
	calls := 0
	_, _, err := Api(testClient(func(*http.Request) (*http.Response, error) { calls++; return response(503, ""), nil }), p)
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
