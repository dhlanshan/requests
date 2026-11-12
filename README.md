# requests

面向 Go 1.26.0 及以上版本的 HTTP 请求库，支持连接复用、超时、重试、业务校验和日志，并提供常见 Body 构造工具。

**完整说明：[功能与架构文档](docs/功能与架构.md)**，包含参数、架构、流与连接生命周期、性能设计、安全策略、接口约定及验证结果。

## 快速使用

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/dhlanshan/requests"
    "github.com/dhlanshan/requests/tn"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    body, header, err := requests.ApiContext(ctx, nil, tn.ApiParam{
        Url:           "https://example.com/api",
        Method:        "GET",
        Params:        map[string]string{"page": "1"},
        SingleTimeout: 3*time.Second,
        Retry:         2, // 首次请求之外最多再尝试两次
    })
    if err != nil {
        fmt.Printf("request failed: %v\n", err)
        return
    }
    fmt.Println(header.Get("Content-Type"), string(body))
}
```

`Api(nil, p)` 使用共享默认 Client；`ApiContext` 可继承上游取消和期限。两者都返回 `([]byte, http.Header, error)`。

## 能力与默认值

- 支持 GET、POST、PUT、PATCH、DELETE、HEAD、OPTIONS、TRACE、CONNECT。
- 支持 URL 参数、请求头、CookieJar、自定义重定向和中间件。
- 提供 JSON、XML、文本、二进制、MsgPack、URL 表单和流式 multipart。
- 默认接受全部 2xx，保留错误响应的 Body/Header；业务校验仍可选。
- 单次默认 30 秒；响应默认限制 16 MiB，错误正文 64 KiB，未知流的重试缓存 8 MiB。
- `ApiTo` 使用固定缓冲区将成功响应写入 Writer，适合大文件下载；写入开始后失败不自动重试。
- 默认共享预算为 64 个在途调用、256 MiB 正文容量；按上限预留，普通请求默认最多同时执行 16 个。
- 命名客户端使用容量为 100 的并发安全 LRU；同名且配置相符才复用，冲突明确返回错误。
- `GetClient` 只取已有命名客户端；`NewClientUncached` 每次创建独立客户端。
- 日志默认只输出脱敏元数据；可显式启用有界 JSON/表单预览。
- `NewPublicClient` 为外部 URL 创建独立的公网客户端，校验实际拨号 IP，支持精确域名白名单。
- 默认拒绝跨源跳转和任意一跳的 HTTPS 降级；跨源正文重放需要独立授权。
- `tc.FileBodyFactoryUnder(root, relative)` 使用 os.Root 将文件限制在授权目录中，防止路径及符号链接逃逸。

## 接口约定

本版不提供旧签名或旧中间件 Context 协议的兼容入口：

- `NewClient(cmd)` 返回 `(*http.Client, error)`，配置错误在创建阶段返回。
- `Validator` 统一采用 `func(context.Context, []byte, http.Header) (bool, error)`，回调应响应取消和期限。
- 日志、重试和业务校验统一由执行层处理；自定义中间件仅通过客户端的 Transport 链接入。

- POST 等非 GET/HEAD/OPTIONS 请求，重试需要 `AllowUnsafeRetry: true` 或服务端支持的非空 `Idempotency-Key`；仅依赖幂等键获准时，跳转丢失键后停止后续重试。
- 业务校验失败默认不重试；临时业务状态可设置 `RetryValidation: true`，永久错误用 `tn.NoRetry(err)` 标记。
- 跨源跳转默认只保留 Accept、Accept-Language、Content-Type、User-Agent；需要其他头时显式配置 `RedirectHeaderAllowlist`，目标服务也必须获得授权。
- 所有时间使用 `time.Duration`：例如 `2*time.Second`，不要写成 `2`。
- 大响应优先使用 `ApiTo`；缓冲返回需同时满足 `MaxResponseBytes` 和共享 Limiter 预算。大文件上传重试使用 `BodyFactory` / `tc.FileBodyFactory` 或 multipart 文件路径。
- 显式 `Content-Length: 0` 会确认正文为空，非空正文在发送前报错；通常由库自动计算长度即可。
- `EchoReq/EchoRes` 不再输出原始凭证和无限长 Body；内置日志、重试策略已集中到 `Api/ApiContext`。
- 命名客户端的回调或工厂无法可靠比较；复用使用 `GetClient`，替换使用 `Reset`，动态配置使用 `NewClientUncached`。
- `Middlewares` 中的节点必须实现 `interfaces.Cloner`；也可使用 `MiddlewareFactories` 构造独立节点。

详见文档中的[接口约定](docs/功能与架构.md#接口约定)。

## 构建和验证

本版代码以 Go 1.26.0 及以上版本为目标，已移除旧工具链的实现分支。`go.mod` 的版本声明及本机工具链由维护者另行升级，依赖本库的主模块也需要设置相应的构建版本。

```sh
go test ./... -skip '^TestK1$'
go test -race ./... -skip '^TestK1$'
go vet ./...
go test ./... -run '^$' -bench . -benchmem

# 安装在开发环境，未加入本库的运行时依赖
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

自动化回归使用本机 `httptest` 服务和内存 Transport；上述命令跳过手动访问外网的 `TestK1`。
