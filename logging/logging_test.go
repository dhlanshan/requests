package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDefaultRedactorProtectsSnapshots(t *testing.T) {
	r := RequestSnapshot{Method: "POST", URL: "https://user:pass@example.test/secret?token=hidden", ContentType: "application/json",
		Header: http.Header{"Authorization": {"credential"}, "X-Custom": {"private"}, "Accept": {"a", "b"}},
		Body:   BodySnapshot{Complete: true, Data: []byte(`{"password":"secret","nested":{"mobile":"123"},"n":9007199254740993}`)}}
	out := (DefaultRedactor{SensitiveFields: []string{"mobile"}}).RedactRequest(r.Clone())
	if out.URL != "https://example.test" || out.Header.Get("Authorization") != "[REDACTED]" || len(out.Header.Values("Accept")) != 2 {
		t.Fatalf("unexpected metadata: %#v", out)
	}
	if strings.Contains(string(out.Body.Data), "secret") || strings.Contains(string(out.Body.Data), `"123"`) || !bytes.Contains(out.Body.Data, []byte("9007199254740993")) {
		t.Fatalf("unexpected redacted body: %s", out.Body.Data)
	}
	if r.Header.Get("Authorization") != "credential" || !bytes.Contains(r.Body.Data, []byte("secret")) {
		t.Fatal("redaction modified the original snapshot")
	}
}

func TestDefaultRedactorOmitsUnsafeBodies(t *testing.T) {
	for _, tc := range []struct {
		name, contentType string
		body              BodySnapshot
	}{
		{"incomplete", "application/json", BodySnapshot{Data: []byte(`{"token":"secret"}`)}},
		{"truncated", "application/json", BodySnapshot{Complete: true, Truncated: true, Data: []byte(`{"token":"secret"}`)}},
		{"invalid", "application/json", BodySnapshot{Complete: true, Data: []byte(`{"token":`)}},
		{"multiple_json", "application/json", BodySnapshot{Complete: true, Data: []byte(`{} {"token":"secret"}`)}},
		{"unsupported", "text/plain", BodySnapshot{Complete: true, Data: []byte("secret")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := (DefaultRedactor{}).RedactResponse(ResponseSnapshot{ContentType: tc.contentType, Body: tc.body})
			if len(r.Body.Data) != 0 || r.Body.OmittedReason == "" {
				t.Fatalf("unsafe body retained: %#v", r.Body)
			}
		})
	}
	form := (DefaultRedactor{}).RedactRequest(RequestSnapshot{ContentType: "application/x-www-form-urlencoded", Body: BodySnapshot{Complete: true, Data: []byte("token=secret&ok=yes")}})
	if bytes.Contains(form.Body.Data, []byte("secret")) || !bytes.Contains(form.Body.Data, []byte("ok=yes")) {
		t.Fatal(string(form.Body.Data))
	}
}

func TestCallCopiesSnapshotsAndEndsOnce(t *testing.T) {
	ctx := context.Background()
	request := RequestSnapshot{URL: "https://example.test/path", Header: http.Header{"X-Test": {"original"}}, Body: BodySnapshot{Complete: true, Data: []byte("payload")}}
	response := ResponseSnapshot{Header: http.Header{"X-Test": {"original"}}, Body: BodySnapshot{Complete: true, Data: []byte("response")}}
	ended := 0
	call := Start(ctx, &Config{Redactor: RedactorFuncs{}, Logger: LoggerFunc(func(context.Context, CallInfo) Session {
		return SessionFuncs{AttemptEnd: func(_ context.Context, r AttemptResult) {
			r.Request.Header.Set("X-Test", "changed")
			r.Request.Body.Data[0] = 'X'
			r.Response.Header.Set("X-Test", "changed")
			r.Response.Body.Data[0] = 'X'
		}, End: func(context.Context, CallResult) { ended++ }}
	})}, CallInfo{})
	call.OnAttemptEnd(ctx, AttemptResult{Request: request, Response: &response})
	call.OnEnd(ctx, CallResult{})
	call.OnEnd(ctx, CallResult{})
	if ended != 1 || request.Header.Get("X-Test") != "original" || string(request.Body.Data) != "payload" || response.Header.Get("X-Test") != "original" || string(response.Body.Data) != "response" {
		t.Fatal("snapshot mutation escaped the session or end was duplicated")
	}
}

func TestExtensionPanicsDoNotEscape(t *testing.T) {
	errorsReported, ended := 0, 0
	call := Start(context.Background(), &Config{OnError: func(_ context.Context, err error) {
		errorsReported++
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatal("panic value leaked")
		}
	}, Logger: LoggerFunc(func(context.Context, CallInfo) Session {
		return SessionFuncs{AttemptEnd: func(context.Context, AttemptResult) { panic("SECRET") }, End: func(context.Context, CallResult) { ended++ }}
	})}, CallInfo{})
	call.OnAttemptEnd(context.Background(), AttemptResult{})
	call.OnEnd(context.Background(), CallResult{})
	if errorsReported != 1 || ended != 1 {
		t.Fatalf("errors=%d ended=%d", errorsReported, ended)
	}
	// 错误报告函数的 panic 同样不能污染业务调用。
	Start(context.Background(), &Config{OnError: func(context.Context, error) { panic("report") }, Logger: LoggerFunc(func(context.Context, CallInfo) Session { panic("start") })}, CallInfo{})
}

func TestRedactorFailureIsFailClosed(t *testing.T) {
	var captured AttemptResult
	call := Start(context.Background(), &Config{
		OnError:  func(context.Context, error) {},
		Redactor: RedactorFuncs{Request: func(RequestSnapshot) RequestSnapshot { panic("secret") }},
		Logger: LoggerFunc(func(_ context.Context, info CallInfo) Session {
			if info.URL != "[omitted]" {
				t.Fatal(info.URL)
			}
			return SessionFuncs{AttemptEnd: func(_ context.Context, r AttemptResult) { captured = r }}
		}),
	}, CallInfo{URL: "https://user:secret@example.test/private"})
	call.OnAttemptEnd(context.Background(), AttemptResult{Request: RequestSnapshot{Header: http.Header{"Token": {"secret"}}, Body: BodySnapshot{Data: []byte("secret")}}})
	if captured.Request.Header != nil || len(captured.Request.Body.Data) != 0 || captured.Request.Body.OmittedReason != "redactor_failed" {
		t.Fatal(captured.Request)
	}
}

func TestConfigAndDisabledLogger(t *testing.T) {
	var logger *SlogLogger
	for _, config := range []*Config{{Logger: logger}, {Capture: CaptureOptions{RequestBodyLimit: -1}}, {Capture: CaptureOptions{ResponseBodyLimit: math.MaxInt64}}} {
		if config.Validate() == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	var output bytes.Buffer
	call := Start(context.Background(), &Config{Logger: NewSlogLogger(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn}))), Capture: CaptureOptions{RequestBody: true}}, CallInfo{})
	if call != nil || call.Capture().RequestBody || output.Len() != 0 {
		t.Fatal("disabled logger created a session")
	}
}

func TestSlogReportsLifecycleWithoutRawError(t *testing.T) {
	var output bytes.Buffer
	call := Start(context.Background(), &Config{Logger: NewSlogLogger(slog.New(slog.NewJSONHandler(&output, nil)))}, CallInfo{RequestID: "R1", TraceID: "trace-1"})
	err := errors.New("SECRET error text")
	call.OnAttemptEnd(context.Background(), AttemptResult{Attempt: 1, Phase: PhaseSend, Err: err})
	call.OnRetry(context.Background(), RetryEvent{Attempt: 1, NextAttempt: 2, Err: err})
	call.OnEnd(context.Background(), CallResult{AttemptCount: 1, LimiterWaitDuration: 12 * time.Millisecond, Err: err})
	if strings.Count(output.String(), "\n") != 4 || strings.Contains(output.String(), "SECRET") || !strings.Contains(output.String(), "http.retry") {
		t.Fatal(output.String())
	}
	decoder := json.NewDecoder(&output)
	sawEnd := false
	for range 4 {
		var event struct {
			Message string        `json:"msg"`
			Wait    time.Duration `json:"limiter_wait_duration"`
		}
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Message == "http.call.end" {
			sawEnd = true
			if event.Wait != 12*time.Millisecond {
				t.Fatal("final log lost the limiter acquisition duration", event)
			}
		}
	}
	if !sawEnd {
		t.Fatal("final log event missing")
	}
}
