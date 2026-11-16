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
	// Middlewares 中的节点必须实现 Cloner；也可直接采用工厂，避免共享可变下游。
	MiddlewareFactories []interfaces.MiddlewareFactory `json:"-"`
	CheckRedirect       CheckRedirectFunc              `json:"-"`
	// Reset 替换命名客户端，关闭旧的空闲连接，不中断在途请求。
	Reset         bool           `json:"reset"`
	NetworkPolicy *NetworkPolicy `json:"network_policy,omitempty"`
	// MaxConnsPerHost 限制内置 Transport 对单个目标的连接总数；0 不设上限，负数无效。
	// 计入正在建立、正在使用和空闲的连接；达到上限时等待可用连接。
	// 它限制连接数量，ApiParam.Limiter 限制调用数量，两者独立；自定义 Transport 遵循自身配置。
	MaxConnsPerHost int `json:"max_conns_per_host"`
}
