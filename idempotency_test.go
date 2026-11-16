package requests

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dhlanshan/requests/tn"
)

// 用真实 Transport 复现连接复用后的响应丢失；假的 RoundTripper 不会触发标准库重发。
func TestIdempotencyKeysAndTransportReplay(t *testing.T) {
	for _, test := range []struct {
		name      string
		header    map[string]string
		wantPosts int32
	}{
		{name: "absent", wantPosts: 1},
		{name: "empty", header: map[string]string{"Idempotency-Key": ""}, wantPosts: 1},
		{name: "blank", header: map[string]string{"idempotency-key": " \t "}, wantPosts: 1},
		{name: "x_empty", header: map[string]string{"X-Idempotency-Key": ""}, wantPosts: 1},
		{name: "x_blank", header: map[string]string{"x-idempotency-key": " \t "}, wantPosts: 1},
		{name: "both_blank", header: map[string]string{"Idempotency-Key": " ", "X-Idempotency-Key": ""}, wantPosts: 1},
		{name: "valid", header: map[string]string{"Idempotency-Key": "operation-1"}, wantPosts: 2},
		{name: "x_valid", header: map[string]string{"X-Idempotency-Key": "operation-1"}, wantPosts: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var posts atomic.Int32
			warmed := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					warmed <- r.RemoteAddr
					_, _ = io.WriteString(w, "warm")
					return
				}
				defer r.Body.Close()
				payload, err := io.ReadAll(r.Body)
				if err != nil || string(payload) != "order" {
					t.Errorf("payload=%q err=%v", payload, err)
				}
				if posts.Add(1) == 1 {
					if first := <-warmed; r.RemoteAddr != first {
						t.Errorf("POST did not reuse warm connection: got=%s want=%s", r.RemoteAddr, first)
					}
					// 服务端已接收操作，但响应丢失；只允许真正有幂等保护的请求重发。
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				_, _ = io.WriteString(w, "created")
			}))
			defer server.Close()
			transport := &http.Transport{MaxIdleConnsPerHost: 1}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			warm, err := client.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, readErr := io.Copy(io.Discard, warm.Body)
			closeErr := warm.Body.Close()
			if readErr != nil || closeErr != nil {
				t.Fatalf("warm response: read=%v close=%v", readErr, closeErr)
			}
			before := maps.Clone(test.header)
			_, _, err = Api(client, tn.ApiParam{
				Url: server.URL, Method: http.MethodPost, Body: strings.NewReader("order"),
				Header: test.header, Retry: 0,
			})
			if posts.Load() != test.wantPosts || (err == nil) != (test.wantPosts == 2) {
				t.Fatalf("server posts=%d want=%d err=%v", posts.Load(), test.wantPosts, err)
			}
			if !maps.Equal(before, test.header) {
				t.Fatal("request construction modified caller headers")
			}
		})
	}
}

func TestIdempotencyKeysControlExplicitRetry(t *testing.T) {
	for _, key := range []string{"idempotency-key", "x-idempotency-key"} {
		for _, value := range []string{"", " \t ", "operation-1", " operation-1 "} {
			t.Run(key+"/"+value, func(t *testing.T) {
				p := params()
				p.Method, p.Body, p.Retry = http.MethodPost, strings.NewReader("order"), 1
				p.Header = map[string]string{key: value}
				valid := strings.TrimSpace(value) != ""
				calls := 0
				_, _, err := Api(testClient(func(req *http.Request) (*http.Response, error) {
					calls++
					_, present := req.Header[http.CanonicalHeaderKey(key)]
					if present != valid || (valid && req.Header.Get(key) != value) {
						t.Errorf("unexpected idempotency header: %v", req.Header)
					}
					if calls == 1 {
						return response(http.StatusServiceUnavailable, "retry"), nil
					}
					return response(http.StatusOK, "ok"), nil
				}), p)
				want := 1
				if valid {
					want = 2
				}
				if calls != want || (err == nil) != valid {
					t.Fatalf("attempts=%d want=%d err=%v", calls, want, err)
				}
			})
		}
	}
}

func TestRedirectIdempotencyKeysAreSanitized(t *testing.T) {
	for _, key := range []string{"Idempotency-Key", "X-Idempotency-Key"} {
		for _, test := range []struct {
			name   string
			values []string
			want   []string
		}{
			{name: "nil"},
			{name: "empty", values: []string{""}},
			{name: "blank", values: []string{" \t "}},
			{name: "mixed", values: []string{"", "operation-1", " \t "}, want: []string{"operation-1"}},
			{name: "valid", values: []string{"operation-1"}, want: []string{"operation-1"}},
		} {
			t.Run(key+"/"+test.name, func(t *testing.T) {
				originCalls, targetCalls := 0, 0
				client := testClient(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == "/path" {
						originCalls++
						res := response(http.StatusTemporaryRedirect, "")
						res.Header.Set("Location", "/target")
						return res, nil
					}
					targetCalls++
					if got, present := req.Header[key]; !reflect.DeepEqual(got, test.want) || present != (test.want != nil) {
						t.Errorf("redirect header=%#v present=%t want=%#v", got, present, test.want)
					}
					if _, present := req.Header[strings.ToLower(key)]; present {
						t.Error("redirect retained a noncanonical blank idempotency header")
					}
					if targetCalls == 1 {
						return response(http.StatusServiceUnavailable, "retry"), nil
					}
					return response(http.StatusOK, "ok"), nil
				})
				client.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
					req.Header[key] = test.values
					req.Header[strings.ToLower(key)] = []string{" "}
					return nil
				}
				p := params()
				p.Method, p.Body, p.Retry = http.MethodPost, strings.NewReader("order"), 1
				p.Header = map[string]string{key: "operation-1"}
				_, _, err := Api(client, p)
				wantAttempts := 1
				if test.want != nil {
					wantAttempts = 2
				}
				if originCalls != wantAttempts || targetCalls != wantAttempts || (err == nil) != (test.want != nil) {
					t.Fatalf("origin=%d target=%d want=%d err=%v", originCalls, targetCalls, wantAttempts, err)
				}
			})
		}
	}
}
