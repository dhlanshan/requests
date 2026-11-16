package core

import (
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/mdw"
	"github.com/dhlanshan/requests/tn"
)

var defaultClient *http.Client
var clientOnce sync.Once
var opaqueConfigID atomic.Uint64

// NewClient 复用配置相符的命名客户端；冲突明确返回错误。
// 闭包和函数型 Transport 无法可靠比较，重复创建请使用 Uncached 或显式 GetClient。
func NewClient(cmd tn.ClientParam) (*http.Client, error) {
	policy, network, err := validateClient(cmd)
	if err != nil {
		return nil, err
	}
	key := cmd.SpaceName
	if key == "" {
		key = "master"
	}
	return clientStore.getOrCreate(key, clientSignature(cmd, network), cmd.Reset, func() (*http.Client, error) { return buildClient(cmd, policy) })
}

// NewClientUncached 不访问全局缓存，每次创建独立客户端；SpaceName/Reset 不参与其行为。
func NewClientUncached(cmd tn.ClientParam) (*http.Client, error) {
	policy, _, err := validateClient(cmd)
	if err != nil {
		return nil, err
	}
	return buildClient(cmd, policy)
}

func GetClient(name string) (*http.Client, bool) {
	if name == "" {
		name = "master"
	}
	return clientStore.Load(name)
}

func validateClient(cmd tn.ClientParam) (*tn.NetworkPolicy, string, error) {
	if cmd.Transport != nil && utils.IsNil(cmd.Transport) {
		return nil, "", fmt.Errorf("%w: nil transport", tn.ErrClientConfig)
	}
	if cmd.Jar != nil && utils.IsNil(cmd.Jar) {
		return nil, "", fmt.Errorf("%w: nil cookie jar", tn.ErrClientConfig)
	}
	if cmd.Timeout < 0 || cmd.MaxConnsPerHost < 0 {
		return nil, "", fmt.Errorf("%w: client timeout and connection limit cannot be negative", tn.ErrClientConfig)
	}
	policy, signature, err := normalizePolicy(cmd.NetworkPolicy)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", tn.ErrClientConfig, err)
	}
	if policy != nil && cmd.Transport != nil {
		return nil, "", fmt.Errorf("%w: NetworkPolicy requires the built-in transport", tn.ErrClientConfig)
	}
	return policy, signature, nil
}

func buildClient(cmd tn.ClientParam, policy *tn.NetworkPolicy) (*http.Client, error) {
	transport := cmd.Transport
	if transport == nil {
		base := defaultTransport(cmd.MaxConnsPerHost)
		if policy != nil {
			transport = secureTransport(base, policy)
		} else {
			transport = base
		}
	}
	chain := mdw.NewMdwChain(transport)
	chain.Middlewares = cmd.Middlewares
	chain.Factories = cmd.MiddlewareFactories
	wrapped, err := chain.Build()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", tn.ErrClientConfig, err)
	}
	return &http.Client{Transport: wrapped, Timeout: cmd.Timeout, Jar: cmd.Jar, CheckRedirect: cmd.CheckRedirect}, nil
}

func configIdentity(value any) string {
	if utils.IsNil(value) {
		return "nil"
	}
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Pointer {
		return fmt.Sprintf("%T:%x", value, v.Pointer())
	}
	// 函数代码地址不能区分捕获了不同参数的闭包，保守地认定为不同配置。
	return fmt.Sprintf("opaque:%d", opaqueConfigID.Add(1))
}

func clientSignature(cmd tn.ClientParam, network string) string {
	var key strings.Builder
	// 0 代表不限制，与显式设置 256 是不同配置，命名客户端不能把两者当作等价。
	fmt.Fprintf(&key, "%d|%d|%q|%s|%s", cmd.Timeout, cmd.MaxConnsPerHost, network, configIdentity(cmd.Transport), configIdentity(cmd.Jar))
	for _, mw := range cmd.Middlewares {
		fmt.Fprintf(&key, "|mw:%s", configIdentity(mw))
	}
	if cmd.CheckRedirect != nil || len(cmd.MiddlewareFactories) > 0 {
		fmt.Fprintf(&key, "|callbacks:%d", opaqueConfigID.Add(1))
	}
	return key.String()
}

// 默认保留连接复用与空闲池配置，不额外设置每个目标的连接总数上限。
// maxConns 的 0 沿用 net/http 的不限制语义，正值由使用者显式启用。
func defaultTransport(maxConns int) *http.Transport {
	return &http.Transport{
		Proxy:        http.ProxyFromEnvironment,
		DialContext:  (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns: 500, MaxIdleConnsPerHost: 100, MaxConnsPerHost: maxConns,
		IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
		ExpectContinueTimeout: time.Second, ForceAttemptHTTP2: true,
	}
}
func NewDefaultClient() *http.Client {
	clientOnce.Do(func() { defaultClient = &http.Client{Transport: defaultTransport(0)} })
	return defaultClient
}
