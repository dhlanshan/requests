# requests

面向 Go 1.26.2 及以上版本的 HTTP 请求库，支持连接复用、超时、重试、业务校验和日志，并提供常见 Body 构造工具。模块最低版本以 go.mod 为准，实际构建建议使用维护分支的最新安全补丁。

**完整说明：[功能与架构文档](docs/功能与架构.md)**，包含参数、架构、流与连接生命周期、性能设计、安全策略、接口约定及验证结果。

会话记录：[会话问答记录](docs/会话问答记录.md)，按顺序保留讨论、代码示例、方案及实施结果，后续问答持续追加到同一文件。

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
- 默认不启用共享并发或正文容量限制；需要时通过 `ApiParam.Limiter` 显式指定共享实例。
- 内置 Transport 的 `MaxConnsPerHost` 默认 0，表示不设连接总数上限；继续复用连接和管理空闲池。
- 命名客户端使用容量为 100 的并发安全 LRU；同名且配置相符才复用，冲突明确返回错误。
- `GetClient` 只取已有命名客户端；`NewClientUncached` 每次创建独立客户端。
- 日志默认关闭；独立的 `logging` 包提供可替换日志协议、内置 slog 和有界正文采集。
- `NewPublicClient` 为外部 URL 创建独立的公网客户端，校验实际拨号 IP，支持精确域名白名单。
- 默认拒绝跨源跳转和任意一跳的 HTTPS 降级；跨源正文重放需要独立授权。
- `tc.FileBodyFactoryUnder(root, relative)` 使用 os.Root 将文件限制在授权目录中，防止路径及符号链接逃逸。

## 接口约定

本版不提供旧签名或旧中间件 Context 协议的兼容入口：

- `NewClient(cmd)` 返回 `(*http.Client, error)`，配置错误在创建阶段返回。
- `Validator` 统一采用 `func(context.Context, []byte, http.Header) (bool, error)`，回调应响应取消和期限。
- 日志、重试和业务校验统一由执行层处理；自定义中间件仅通过客户端的 Transport 链接入。

- POST 等非 GET/HEAD/OPTIONS 请求，重试需要 `AllowUnsafeRetry: true` 或服务端支持的非空白 `Idempotency-Key` / `X-Idempotency-Key`；空值键不发送，仅依赖幂等键获准时，跳转丢失键后停止后续重试。`Retry` 只统计执行层尝试，标准 Transport 仍可能对具有有效幂等键且正文可重放的请求透明重发。
- 业务校验失败默认不重试；临时业务状态可设置 `RetryValidation: true`，永久错误用 `tn.NoRetry(err)` 标记。失败与超时同时发生时保留两个原因，禁止重试标记继续生效。
- 跨源跳转默认只保留 Accept、Accept-Language、Content-Type、User-Agent；返回最初站点也执行清理。需要其他头时显式配置 `RedirectHeaderAllowlist`，目标服务也必须获得授权。
- 所有时间使用 `time.Duration`：例如 `2*time.Second`，不要写成 `2`。
- 大响应优先使用 `ApiTo`；缓冲返回受 `MaxResponseBytes` 限制，显式启用共享容量限制时还需满足对应预算。大文件上传重试使用 `BodyFactory` / `tc.FileBodyFactory` 或 multipart 文件路径。
- 显式 `Content-Length: 0` 会确认正文为空，非空正文在发送前报错；通常由库自动计算长度即可。
- 日志统一通过 `ApiParam.Logging` 配置，不保留旧的日志开关；详见[日志模块](logging/README.md)。
- 命名客户端的回调或工厂无法可靠比较；复用使用 `GetClient`，替换使用 `Reset`，动态配置使用 `NewClientUncached`。
- `Middlewares` 中的节点必须实现 `interfaces.Cloner`；也可使用 `MiddlewareFactories` 构造独立节点。

详见文档中的[接口约定](docs/功能与架构.md#接口约定)。

## 显式启用并发控制

`ApiParam.Limiter` 为 nil 时，跳过共享预算计算和名额申请。单次响应、重试缓存与日志正文的大小上限仍然独立生效；总体并发和内存规模由应用程序控制。

```go
shared, err := tn.NewLimiter(100, 0) // 最多 100 个调用，不限制共享正文容量。
if err != nil {
    return err
}
pA.Limiter = shared // A、B 共用一个实例，合计最多 100 个调用。
pB.Limiter = shared
pC.Limiter = nil    // C 不参与这组限制。
```

实例应创建后复用。`NewLimiter(100, 512<<20)` 还会启用 512 MiB 的共享正文预算，按配置上限预留，实际可并发数量由两项限制共同决定。名额从正文准备前持有到整次调用退出，各轮重试及重试等待共用一个名额。等待可取消，计入总 Timeout；`CallResult.LimiterWaitDuration` 单独记录取得名额的耗时。

本版已移除 `SetDefaultLimiter`；需要统一限制时，在业务自己的统一请求方法中显式传入共享实例。

## 日志

```go
// nil 关闭日志；空配置使用内置 slog，只记录生命周期和基础元数据。
p.Logging = &logging.Config{}

// 替换为业务自己的输出；每次调用由 Start 创建独立 Session。
p.Logging = &logging.Config{
    Logger: logging.LoggerFunc(func(ctx context.Context, info logging.CallInfo) logging.Session {
        return logging.SessionFuncs{
            End: func(ctx context.Context, result logging.CallResult) {
                // 接入公司的日志方法；info.TraceID、result.RetryCount、result.Duration 等均可使用。
            },
        }
    }),
}
```

导入 `github.com/dhlanshan/requests/logging`。`OnAttemptEnd` 提供每次尝试的请求、响应、耗时和错误，`OnRetry` 提供重试计划，`OnEnd` 提供最终汇总。请求头和正文需显式开启采集；默认脱敏策略仍生效。完整配置、事件顺序、正文限制及自定义日志示例见[日志模块说明](logging/README.md)。

## 构建和验证

本版最低要求 Go 1.26.2，与 `go.mod` 一致，已移除旧工具链的实现分支。实际构建工具链由维护者更新至维护分支的安全补丁；依赖本库的主模块也需要设置相应的构建版本。

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
