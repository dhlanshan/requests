package requests

import (
	"net/http"

	"github.com/dhlanshan/requests/internal/core"
	"github.com/dhlanshan/requests/tn"
)

// NewClient 取得命名客户端；配置冲突会安全失败，更新配置需设置 Reset。
func NewClient(cmd tn.ClientParam) *http.Client { return core.NewClient(cmd) }

// NewClientChecked 在创建阶段返回无效网络策略或中间件配置错误。
func NewClientChecked(cmd tn.ClientParam) (*http.Client, error) { return core.NewClientChecked(cmd) }

// NewClientUncached 每次创建独立客户端，适合租户隔离和包含闭包的动态配置。
func NewClientUncached(cmd tn.ClientParam) (*http.Client, error) { return core.NewClientUncached(cmd) }

// GetClient 显式取得已经创建的命名客户端，不重新提交配置；空名称表示 master。
func GetClient(name string) (*http.Client, bool) { return core.GetClient(name) }

// NewPublicClient 为外部输入 URL 创建独立的公网客户端，强制检查实际拨号 IP 并禁用环境代理。
// AllowedHosts 可进一步限制目标；路径、端口等业务权限仍使用 ApiParam.URLPolicy。
func NewPublicClient(cmd tn.ClientParam) (*http.Client, error) {
	policy := &tn.NetworkPolicy{BlockPrivateIPs: true}
	if cmd.NetworkPolicy != nil {
		policy.AllowedHosts = append([]string(nil), cmd.NetworkPolicy.AllowedHosts...)
	}
	cmd.NetworkPolicy = policy
	return core.NewClientUncached(cmd)
}
