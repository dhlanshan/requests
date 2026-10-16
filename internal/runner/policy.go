package runner

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/dhlanshan/requests/tn"
)

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func redirectHeaders(p tn.ApiParam) map[string]bool {
	list := p.RedirectHeaderAllowlist
	if list == nil {
		list = []string{"Accept", "Accept-Language", "Content-Type", "User-Agent"}
	}
	allowed := make(map[string]bool, len(list))
	for _, name := range list {
		allowed[http.CanonicalHeaderKey(name)] = true
	}
	return allowed
}

func checkRedirectTarget(req *http.Request, first, previous *url.URL, p tn.ApiParam) error {
	deny := func(reason string) error { return fmt.Errorf("%w: %s", tn.ErrTargetBlocked, reason) }
	if req.URL == nil || req.URL.User != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return deny("invalid redirect URL")
	}
	if previous.Scheme == "https" && req.URL.Scheme == "http" && !p.AllowHTTPSDowngrade {
		return deny("HTTPS downgrade denied")
	}
	if !sameOrigin(first, req.URL) {
		if !p.AllowCrossOriginRedirect {
			return deny("cross-origin redirect denied")
		}
		if req.Body != nil && req.Body != http.NoBody && !p.AllowCrossOriginBody {
			return deny("cross-origin body replay denied")
		}
		allowed := redirectHeaders(p)
		for name := range req.Header {
			if !allowed[http.CanonicalHeaderKey(name)] {
				delete(req.Header, name)
			}
		}
	}
	if p.URLPolicy != nil {
		copyURL := *req.URL
		if err := p.URLPolicy(req.Context(), &copyURL); err != nil {
			return fmt.Errorf("%w: %w", tn.ErrTargetBlocked, err)
		}
	}
	return req.Context().Err()
}

// originJar 阻止标准 CookieJar 在显式允许的跨源跳转后自动补回 Cookie。
// 用户只有显式将 Cookie 放入跨源头白名单，才会继续采用 Jar 的域规则。
type originJar struct {
	base       http.CookieJar
	first      url.URL
	allowCross bool
}

func (j *originJar) Cookies(u *url.URL) []*http.Cookie {
	if !j.allowCross && !sameOrigin(&j.first, u) {
		return nil
	}
	return j.base.Cookies(u)
}
func (j *originJar) SetCookies(u *url.URL, c []*http.Cookie) { j.base.SetCookies(u, c) }

// policyClient 复制 Client 并复用原连接池；用户回调执行后再检查最终目标。
func policyClient(client *http.Client, p tn.ApiParam, first *url.URL, retryAllowed *bool) *http.Client {
	copyClient := *client
	if client.Jar != nil {
		copyClient.Jar = &originJar{base: client.Jar, first: *first, allowCross: redirectHeaders(p)["Cookie"]}
	}
	copyClient.CheckRedirect = func(req *http.Request, via []*http.Request) (err error) {
		// 307/308 在检查重定向前已调用 GetBody；所有拒绝分支均关闭尚未发送的新流。
		defer func() {
			if err != nil && req.Body != nil {
				_ = req.Body.Close()
			}
		}()
		if client.CheckRedirect != nil {
			if err := client.CheckRedirect(req, via); err != nil {
				return err
			}
		}
		if len(via) > p.MaxRedirects {
			return fmt.Errorf("%w: redirect limit exceeded", tn.ErrTargetBlocked)
		}
		previous := first
		if len(via) > 0 {
			previous = via[len(via)-1].URL
		}
		if err := checkRedirectTarget(req, first, previous, p); err != nil {
			return err
		}
		// 整次重试会重做所有跳转；任何一跳失去幂等保护后，即使最终变为 GET 也不能重做。
		// 检查位于用户回调和跨源头清理之后，RetryDecider 无法绕过此限制。
		if !canRetry(req, p) {
			*retryAllowed = false
		}
		return nil
	}
	return &copyClient
}
