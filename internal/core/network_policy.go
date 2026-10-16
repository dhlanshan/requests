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

var reservedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

func publicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.Zone() != "" || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range reservedNetworks {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
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
