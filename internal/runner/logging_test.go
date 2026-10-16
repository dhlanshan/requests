package runner

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dhlanshan/requests/tn"
)

type loggingTransport struct{}

func (loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		req.Body.Close()
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"s=COOKIE-SECRET"}}, Body: io.NopCloser(strings.NewReader(`{"token":"RESPONSE-SECRET","ok":true}`))}, nil
}

func TestLogsAreBoundedAndRedacted(t *testing.T) {
	var output bytes.Buffer
	p := tn.ApiParam{Url: "http://example.test/PATH-SECRET?token=QUERY-SECRET", Method: "POST", Body: strings.NewReader(`{"password":"BODY-SECRET","nested":{"access_token":"NESTED-SECRET"},"account":"CUSTOM-SECRET","ok":true}`),
		Header: map[string]string{"Authorization": "HEADER-SECRET", "X-Custom": "UNKNOWN-SECRET", "Content-Type": "application/json"}, SensitiveFields: []string{"account"}, EchoReq: true, EchoRes: true, LogBody: true,
		Logger: slog.New(slog.NewJSONHandler(&output, nil)), Caller: "line\nINJECTED"}
	_, _, err := Execute(context.Background(), &http.Client{Transport: loggingTransport{}}, p)
	if err != nil {
		t.Fatal(err)
	}
	logs := output.String()
	for _, secret := range []string{"PATH-SECRET", "QUERY-SECRET", "HEADER-SECRET", "UNKNOWN-SECRET", "BODY-SECRET", "NESTED-SECRET", "CUSTOM-SECRET", "RESPONSE-SECRET", "COOKIE-SECRET"} {
		if strings.Contains(logs, secret) {
			t.Errorf("secret leaked: %s", secret)
		}
	}
	if strings.Count(logs, "\n") != 2 || !strings.Contains(logs, "REDACTED") {
		t.Fatalf("unexpected log format: %s", logs)
	}
	if got := preview([]byte(`{"password":"secret"}`), "application/json", true, tn.ApiParam{LogBody: true, LogBodyLimit: 8}); got != "[omitted]" {
		t.Fatalf("oversized preview: %s", got)
	}
	if got := preview([]byte("token=secret&ok=yes"), "application/x-www-form-urlencoded", true, tn.ApiParam{LogBody: true, LogBodyLimit: 128}); strings.Contains(got, "secret") || !strings.Contains(got, "ok=yes") {
		t.Fatalf("form preview: %s", got)
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

type bodyInspectTransport struct{ t *testing.T }

func (tr bodyInspectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, ok := req.Body.(*captureBody); ok {
		tr.t.Error("body capture allocated while Info logging is disabled")
	}
	_, _ = io.Copy(io.Discard, req.Body)
	req.Body.Close()
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

func TestDisabledLoggerSkipsCapture(t *testing.T) {
	var output bytes.Buffer
	p := tn.ApiParam{Url: "http://example.test", Method: "POST", Body: strings.NewReader(`{"password":"private"}`), EchoReq: true, LogBody: true, Logger: slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn}))}
	if _, _, err := Execute(context.Background(), &http.Client{Transport: bodyInspectTransport{t}}, p); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("unexpected Info output")
	}
}
