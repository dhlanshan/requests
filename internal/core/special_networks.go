package core

import "net/netip"

// specialNetworkRules 是只读的特殊用途地址快照，按最长前缀决定是否可作为公网目标。
// 来源（注册表更新时间均为 2025-10-09，核对日期 2026-11-16）：
// https://www.iana.org/assignments/iana-ipv4-special-registry/
// https://www.iana.org/assignments/iana-ipv6-special-registry/
// 仅明确允许 Destination 和 Globally Reachable 的例外；空白、N/A 等属性保守拒绝。
// 规则随库发布，不在请求路径下载或修改；普通公网地址仍需先通过基本地址检查。
// NAT64 的标准转换前缀额外拒绝，防止嵌入的非公网 IPv4 绕过目标地址检查；
// 已废弃的 IPv6 site-local 也继续拒绝。这两项是本库的保守策略。
var specialNetworkRules = [...]struct {
	prefix netip.Prefix
	public bool
}{
	{netip.MustParsePrefix("0.0.0.0/8"), false},
	{netip.MustParsePrefix("10.0.0.0/8"), false},
	{netip.MustParsePrefix("100.64.0.0/10"), false},
	{netip.MustParsePrefix("127.0.0.0/8"), false},
	{netip.MustParsePrefix("169.254.0.0/16"), false},
	{netip.MustParsePrefix("172.16.0.0/12"), false},
	{netip.MustParsePrefix("192.0.0.0/24"), false},
	{netip.MustParsePrefix("192.0.0.9/32"), true},
	{netip.MustParsePrefix("192.0.0.10/32"), true},
	{netip.MustParsePrefix("192.0.2.0/24"), false},
	{netip.MustParsePrefix("192.31.196.0/24"), true},
	{netip.MustParsePrefix("192.52.193.0/24"), true},
	{netip.MustParsePrefix("192.88.99.0/24"), false},
	{netip.MustParsePrefix("192.88.99.2/32"), false},
	{netip.MustParsePrefix("192.168.0.0/16"), false},
	{netip.MustParsePrefix("192.175.48.0/24"), true},
	{netip.MustParsePrefix("198.18.0.0/15"), false},
	{netip.MustParsePrefix("198.51.100.0/24"), false},
	{netip.MustParsePrefix("203.0.113.0/24"), false},
	{netip.MustParsePrefix("240.0.0.0/4"), false},
	{netip.MustParsePrefix("::/128"), false},
	{netip.MustParsePrefix("::1/128"), false},
	{netip.MustParsePrefix("64:ff9b::/96"), false},
	{netip.MustParsePrefix("64:ff9b:1::/48"), false},
	{netip.MustParsePrefix("100::/64"), false},
	{netip.MustParsePrefix("100:0:0:1::/64"), false},
	{netip.MustParsePrefix("2001::/23"), false},
	{netip.MustParsePrefix("2001:1::1/128"), true},
	{netip.MustParsePrefix("2001:1::2/128"), true},
	{netip.MustParsePrefix("2001:1::3/128"), true},
	{netip.MustParsePrefix("2001:3::/32"), true},
	{netip.MustParsePrefix("2001:4:112::/48"), true},
	{netip.MustParsePrefix("2001:20::/28"), true},
	{netip.MustParsePrefix("2001:30::/28"), true},
	{netip.MustParsePrefix("2001:db8::/32"), false},
	{netip.MustParsePrefix("2002::/16"), false},
	{netip.MustParsePrefix("2620:4f:8000::/48"), true},
	{netip.MustParsePrefix("3fff::/20"), false},
	{netip.MustParsePrefix("5f00::/16"), false},
	{netip.MustParsePrefix("fc00::/7"), false},
	{netip.MustParsePrefix("fe80::/10"), false},
	{netip.MustParsePrefix("fec0::/10"), false},
}
