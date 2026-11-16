package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"testing"

	"github.com/dhlanshan/requests/tn"
)

func TestPublicAddressSpecialUseBoundaries(t *testing.T) {
	for _, test := range []struct {
		address string
		public  bool
	}{
		{"3ffe:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"3fff::", false},
		{"3fff:fff:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"3fff:1000::", true},
		{"5eff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"5f00::", false},
		{"5f00:ffff:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"5f01::", true},
		{"192.88.98.255", true}, {"192.88.99.0", false},
		{"192.88.99.1", false}, {"192.88.99.2", false},
		{"192.88.99.255", false}, {"192.88.100.0", true},
		{"100:0:0:1::", false},
		{"100:0:0:1:ffff:ffff:ffff:ffff", false},
		{"100:0:0:2::", true},
		{"192.0.0.8", false}, {"192.0.0.9", true},
		{"192.0.0.10", true}, {"192.0.0.11", false},
		{"2001:1::", false}, {"2001:1::1", true},
		{"2001:1::2", true}, {"2001:1::3", true}, {"2001:1::4", false},
		{"2001:3::", true}, {"2001:3:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"2001:4:112::", true}, {"2001:4:113::", false},
		{"2001:20::", true}, {"2001:2f:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"2001:30::", true}, {"2001:3f:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"2001:40::", false}, {"2001:db8::1", false},
		{"64:ff9b::a00:1", false}, {"64:ff9b:1::1", false},
		{"::ffff:192.88.99.2", false}, {"::ffff:10.0.0.1", false},
		{"::ffff:192.0.0.9", true}, {"::ffff:8.8.8.8", true},
		{"2606:4700::1111%eth0", false}, {"::ffff:8.8.8.8%eth0", false},
		{"127.0.0.1", false}, {"10.0.0.1", false}, {"169.254.1.1", false},
		{"fc00::1", false}, {"fe80::1", false}, {"fec0::1", false},
		{"::", false}, {"::1", false}, {"224.0.0.1", false},
		{"8.8.8.8", true}, {"2606:4700::1111", true},
	} {
		t.Run(test.address, func(t *testing.T) {
			if got := publicAddress(netip.MustParseAddr(test.address)); got != test.public {
				t.Fatalf("publicAddress(%s)=%t want=%t", test.address, got, test.public)
			}
		})
	}
	if publicAddress(netip.Addr{}) {
		t.Fatal("invalid address accepted")
	}
}

// 使用字面 IP，不依赖 DNS 或真实连接；白名单仍须通过最终拨号地址检查。
func TestAllowedHostCannotBypassAddressPolicy(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "192.88.99.2", "3fff::1", "5f00::1"} {
		t.Run(host, func(t *testing.T) {
			transport := secureTransport(&http.Transport{}, &tn.NetworkPolicy{BlockPrivateIPs: true, AllowedHosts: []string{host}}).(*guardedTransport)
			defer transport.CloseIdleConnections()
			conn, err := transport.base.DialContext(context.Background(), "tcp", net.JoinHostPort(host, "80"))
			if conn != nil {
				conn.Close()
				t.Fatal("blocked address was dialed")
			}
			if !errors.Is(err, tn.ErrTargetBlocked) {
				t.Fatalf("expected policy refusal, got %v", err)
			}
		})
	}
}
