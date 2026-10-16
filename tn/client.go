package tn

import (
	"github.com/dhlanshan/requests/interfaces"
	"net/http"
	"time"
)

type CheckRedirectFunc func(req *http.Request, via []*http.Request) error

// NetworkPolicy 选择性限制出站访问；nil 保留内网和环境代理能力。
// 启用后禁用环境代理并拒绝自定义 Transport，防止绕过实际拨号 IP 检查。
type NetworkPolicy struct {
	AllowedHosts    []string `json:"allowed_hosts,omitempty"`
	BlockPrivateIPs bool     `json:"block_private_ips"`
}
type ClientParam struct {
	Timeout   time.Duration     `json:"timeout"`
	SpaceName string            `json:"space_name"`
	Transport http.RoundTripper `json:"-"`
	// Jar 为 nil 时不启用 CookieJar；接口中保存的带类型 nil 会被客户端构造器拒绝。
	Jar         http.CookieJar          `json:"-"`
	Middlewares []interfaces.Middleware `json:"-"`
	// MiddlewareFactories 从外到内构造独立节点，位于 Middlewares 外侧。
	// Middlewares 的旧节点必须实现 Cloner；新代码也可直接采用工厂，避免共享可变下游。
	MiddlewareFactories []interfaces.MiddlewareFactory `json:"-"`
	CheckRedirect       CheckRedirectFunc              `json:"-"`
	// Reset 替换命名客户端，关闭旧的空闲连接，不中断在途请求。
	Reset         bool           `json:"reset"`
	NetworkPolicy *NetworkPolicy `json:"network_policy,omitempty"`
	// 0 使用默认 256，限制单个目标连接数。
	MaxConnsPerHost int `json:"max_conns_per_host"`
}
