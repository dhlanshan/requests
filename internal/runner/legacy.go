package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/dhlanshan/requests/internal/lz"
	"github.com/dhlanshan/requests/tn"
)

type legacyKey struct{}

func isLegacy(ctx context.Context) bool { value, _ := ctx.Value(legacyKey{}).(bool); return value }

type legacyTransport struct {
	original *http.Request
	next     http.RoundTripper
	last     *http.Response
}

func (t *legacyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// 原请求的多值 Header、Host、Trailer 等属性保持完整；Body/Context 由统一执行层管理。
	out := t.original.Clone(req.Context())
	out.URL = req.URL
	out.Body = req.Body
	out.GetBody = req.GetBody
	out.ContentLength = req.ContentLength
	response, err := t.next.RoundTrip(out)
	t.last = response
	return response, err
}

// LegacyRoundTrip 适配手动向 Context 注入 ApiParam 的旧中间件用法。
// 3xx 交给外层 Client 跟随；每一跳仍检查初始地址的重定向安全边界。
func LegacyRoundTrip(req *http.Request, p tn.ApiParam, next http.RoundTripper) (*http.Response, error) {
	defaults(&p)
	first := req
	previous := req.URL
	if req.Response != nil && req.Response.Request != nil {
		previous = req.Response.Request.URL
	}
	for hops := 0; first.Response != nil && first.Response.Request != nil; hops++ {
		if hops >= p.MaxRedirects {
			if req.Body != nil {
				_ = req.Body.Close()
			}
			return nil, &tn.RequestError{Err: tn.ErrTargetBlocked}
		}
		first = first.Response.Request
	}
	working := req.Clone(req.Context())
	if working.Body != nil {
		working.Body = lz.Guard(working.Body)
	}
	if err := checkRedirectTarget(working, first.URL, previous, p); err != nil {
		if working.Body != nil {
			_ = working.Body.Close()
		}
		return nil, &tn.RequestError{Err: err}
	}
	p.Url, p.Method, p.Params = req.URL.String(), req.Method, nil
	p.Header = make(map[string]string)
	// Header 的真实多值由 legacyTransport 保留；这里提供日志和方法检查所需的单值视图。
	for key, values := range working.Header {
		if len(values) > 0 {
			p.Header[key] = values[0]
		}
	}
	p.Body, p.BodyFactory = working.Body, nil
	if req.GetBody != nil {
		p.Body = nil
		var opened atomic.Bool
		p.BodyFactory = func(context.Context) (io.ReadCloser, int64, error) {
			if !opened.Swap(true) {
				if working.Body == nil {
					return http.NoBody, 0, nil
				}
				return working.Body, req.ContentLength, nil
			}
			body, err := req.GetBody()
			return body, req.ContentLength, err
		}
		if working.Body != nil {
			defer working.Body.Close()
		}
	}
	success := p.SuccessStatus
	p.SuccessStatus = func(status int) bool {
		if status >= 300 && status <= 399 {
			return true
		}
		if success != nil {
			return success(status)
		}
		return status >= 200 && status <= 299
	}
	transport := &legacyTransport{original: working, next: next}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	body, header, err := Execute(context.WithValue(req.Context(), legacyKey{}, true), client, p)
	// RoundTripper 对完整的 HTTP 错误状态应返回响应；业务/网络/读取失败仍返回 error。
	var statusErr *tn.HTTPError
	if errors.As(err, &statusErr) && err == statusErr {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if transport.last == nil {
		return nil, errors.New("legacy transport returned no response")
	}
	result := *transport.last
	result.Body = io.NopCloser(bytes.NewReader(body))
	result.Header = header
	result.ContentLength = int64(len(body))
	result.Request = req
	return &result, nil
}
