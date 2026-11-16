package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/dhlanshan/requests/tn"
)

func normalizePolicy(input *tn.NetworkPolicy) (*tn.NetworkPolicy, string, error) {
	if input == nil {
		return nil, "", nil
	}
	p := &tn.NetworkPolicy{BlockPrivateIPs: input.BlockPrivateIPs}
	for _, value := range input.AllowedHosts {
		host := strings.ToLower(strings.TrimSuffix(value, "."))
		if !validPolicyHost(host) {
			return nil, "", fmt.Errorf("allowed hosts must be exact ASCII hostnames or IP addresses without ports")
		}
		p.AllowedHosts = append(p.AllowedHosts, host)
	}
	sort.Strings(p.AllowedHosts)
	return p, fmt.Sprintf("%t:%s", p.BlockPrivateIPs, strings.Join(p.AllowedHosts, ",")), nil
}

func validPolicyHost(host string) bool {
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Zone() == ""
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range label {
			if c := label[i]; !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func checkHost(policy *tn.NetworkPolicy, host string) error {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(policy.AllowedHosts) > 0 {
		found := false
		for _, allowed := range policy.AllowedHosts {
			if host == allowed {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: hostname is not allowed", tn.ErrTargetBlocked)
		}
	}
	return nil
}

func publicAddress(addr netip.Addr) bool {
	// Zone 必须在 Unmap 之前检查，不能让映射地址的规范化丢掉作用域信息。
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	public, mostSpecific := true, -1
	for _, rule := range specialNetworkRules {
		if rule.prefix.Bits() > mostSpecific && rule.prefix.Contains(addr) {
			public, mostSpecific = rule.public, rule.prefix.Bits()
		}
	}
	return public
}

type guardedTransport struct {
	base   *http.Transport
	policy *tn.NetworkPolicy
}

func (t *guardedTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }
func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	deny := func(err error) (*http.Response, error) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	if req.URL == nil || req.URL.User != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return deny(fmt.Errorf("%w: invalid target URL", tn.ErrTargetBlocked))
	}
	if err := checkHost(t.policy, req.URL.Hostname()); err != nil {
		return deny(err)
	}
	if req.Host != "" && req.Host != req.URL.Host {
		u := &url.URL{Host: req.Host}
		if err := checkHost(t.policy, u.Hostname()); err != nil {
			return deny(err)
		}
	}
	return t.base.RoundTrip(req)
}

// secureTransport 禁用代理；DNS 结果先检查，再直接拨号到该 IP，
// 不把已检查的域名重新交给 Dialer 解析，避免 DNS 重绑定窗口。
func secureTransport(base *http.Transport, policy *tn.NetworkPolicy) http.RoundTripper {
	base.Proxy = nil
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		// DNS 解析也纳入拨号预算，而不只限制 TCP 建连时间。
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if err := checkHost(policy, host); err != nil {
			return nil, err
		}
		var addresses []netip.Addr
		if literal, err := netip.ParseAddr(host); err == nil {
			addresses = []netip.Addr{literal}
		} else {
			addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("%w: no resolved address", tn.ErrTargetBlocked)
		}
		if policy.BlockPrivateIPs {
			for _, addr := range addresses {
				if !publicAddress(addr) {
					return nil, fmt.Errorf("%w: non-public address", tn.ErrTargetBlocked)
				}
			}
		}
		return dialVerified(ctx, network, port, addresses, 300*time.Millisecond, dialer.DialContext)
	}
	return &guardedTransport{base: base, policy: policy}
}
