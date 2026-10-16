package requests

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhlanshan/requests/interfaces"
	"github.com/dhlanshan/requests/mdw"
	"github.com/dhlanshan/requests/tn"
)

func TestClientDurationCookiesAndRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "yes", Path: "/"})
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		cookie, err := r.Cookie("session")
		if err != nil || cookie.Value != "yes" {
			t.Error("cookie jar was ignored")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	var redirects atomic.Int32
	client, err := NewClientChecked(tn.ClientParam{
		SpaceName: t.Name(), Timeout: 250 * time.Millisecond, Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { redirects.Add(1); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if client.Timeout != 250*time.Millisecond || client.Jar != jar {
		t.Fatal("client configuration not applied")
	}
	p := params()
	p.Url = server.URL + "/start"
	body, _, err := Api(client, p)
	if err != nil || string(body) != "ok" || redirects.Load() != 1 {
		t.Fatalf("body=%q redirects=%d err=%v", body, redirects.Load(), err)
	}
}

func TestBuiltinMiddlewareWorksForPlainClient(t *testing.T) {
	client, err := NewClientChecked(tn.ClientParam{
		SpaceName: t.Name(), Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, "ok"), nil }),
		Middlewares: []interfaces.Middleware{&mdw.LoggingMiddleware{}, &mdw.RetryMiddleware{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get("http://example.test")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err = io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
}

func TestURLPolicyChecksRedirects(t *testing.T) {
	targetCalls := atomic.Int32{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	p := params()
	p.Url = source.URL
	p.URLPolicy = func(_ context.Context, u *url.URL) error {
		if u.String() != source.URL {
			return errors.New("redirect denied")
		}
		return nil
	}
	p.AllowCrossOriginRedirect = true
	_, _, err := Api(source.Client(), p)
	if !errors.Is(err, tn.ErrTargetBlocked) || targetCalls.Load() != 0 {
		t.Fatalf("calls=%d err=%v", targetCalls.Load(), err)
	}
}

func TestCrossOriginRedirectStripsSecrets(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, key := range []string{"Authorization", "Cookie", "X-Api-Key", "X-Company-Secret", "Referer"} {
			if r.Header.Get(key) != "" {
				t.Errorf("%s leaked", key)
			}
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	p := params()
	p.Url = source.URL
	p.AllowCrossOriginRedirect = true
	p.Header = map[string]string{"Authorization": "secret", "Cookie": "a=b", "X-Api-Key": "key", "X-Company-Secret": "private"}
	_, _, err := Api(source.Client(), p)
	if err != nil {
		t.Fatal(err)
	}
}

func TestNetworkPolicyAndConfigurationFailures(t *testing.T) {
	client, err := NewClientChecked(tn.ClientParam{SpaceName: t.Name(), NetworkPolicy: &tn.NetworkPolicy{BlockPrivateIPs: true}})
	if err != nil {
		t.Fatal(err)
	}
	p := params()
	p.Url = "http://127.0.0.1:1"
	_, _, err = Api(client, p)
	if !errors.Is(err, tn.ErrTargetBlocked) {
		t.Fatalf("local target allowed: %v", err)
	}
	_, err = NewClientChecked(tn.ClientParam{SpaceName: t.Name()})
	if err == nil {
		t.Fatal("network policy conflict silently reused client")
	}
	_, err = NewClientChecked(tn.ClientParam{NetworkPolicy: &tn.NetworkPolicy{}, Transport: http.DefaultTransport})
	if err == nil {
		t.Fatal("custom transport bypasses network policy")
	}
	mw := &mdw.RetryMiddleware{}
	_, err = NewClientChecked(tn.ClientParam{SpaceName: t.Name() + "duplicate", Middlewares: []interfaces.Middleware{mw, mw}})
	if err == nil {
		t.Fatal("duplicate middleware accepted")
	}
	bad := NewClient(tn.ClientParam{Timeout: -time.Second})
	_, _, err = Api(bad, params())
	if !errors.Is(err, tn.ErrClientConfig) {
		t.Fatalf("legacy NewClient should fail closed: %v", err)
	}
}

func TestStoppedRedirectClosesReopenedBody(t *testing.T) {
	p := params()
	p.Method = "POST"
	var bodies []*trackingBody
	p.BodyFactory = func(context.Context) (io.ReadCloser, int64, error) {
		body := &trackingBody{Reader: strings.NewReader("payload")}
		bodies = append(bodies, body)
		return body, 7, nil
	}
	p.SuccessStatus = func(status int) bool { return status == 307 }
	client := testClient(func(req *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, req.Body)
		resp := response(307, "")
		resp.Header.Set("Location", "/next")
		return resp, nil
	})
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	_, _, err := Api(client, p)
	if err != nil || len(bodies) != 2 {
		t.Fatalf("bodies=%d err=%v", len(bodies), err)
	}
	for _, body := range bodies {
		if body.closes.Load() != 1 {
			t.Fatalf("redirect body close count=%d", body.closes.Load())
		}
	}
}
