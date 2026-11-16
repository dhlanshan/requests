# logging 日志模块

`github.com/dhlanshan/requests/logging` 是公开的独立包，只依赖 Go 标准库。它与 requests 使用同一个 Go module，不需要单独安装。业务可以实现自己的日志格式或对接已有日志库，也可以脱离 HTTP 执行层自行派发这些事件。

## 目录职责

| 文件 | 职责 |
| --- | --- |
| doc.go | 包级约定、并发与数据所有权说明 |
| config.go | 日志配置、采集选项及参数检查 |
| events.go | 调用信息、尝试结果、重试计划、最终汇总和正文快照 |
| logger.go | Logger / Session 协议、函数适配器、脱敏派发及 panic 隔离 |
| redact.go | 可替换的脱敏协议与保守的默认策略 |
| slog.go | 使用标准库 slog 的内置输出实现 |
| ../internal/runner/logging.go | 执行层桥接、正常读流过程中的有界采集 |

执行层决定何时发送、是否重试、如何判定错误；logging 负责接收事件并输出。日志模块不参与 Transport 中间件建链。

## 开启内置日志

```go
import (
    "context"
    "log/slog"
    "os"

    "github.com/dhlanshan/requests"
    "github.com/dhlanshan/requests/logging"
    "github.com/dhlanshan/requests/tn"
)

// 此配置可以跨调用共享；开始使用后保持只读。
config := &logging.Config{
    Logger: logging.NewSlogLogger(slog.New(slog.NewJSONHandler(os.Stdout, nil))),
    Capture: logging.CaptureOptions{
        RequestHeaders:    true,
        ResponseHeaders:   true,
        RequestBody:       true,
        ResponseBody:      true,
        RequestBodyLimit:  4 << 10,
        ResponseBodyLimit: 4 << 10,
    },
    Redactor: logging.DefaultRedactor{SensitiveFields: []string{"mobile", "id_card"}},
}

_, _, err := requests.ApiContext(context.Background(), nil, tn.ApiParam{
    Url:     "https://example.com/api",
    Method:  "GET",
    TraceId: "trace-from-business",
    Caller:  "order.Query",
    Logging: config,
})
_ = err
```

- `Logging: nil` 关闭日志，也不采集日志正文。
- `Logging: &logging.Config{}` 使用 `slog.Default()`，只记录生命周期和基础元数据。
- 自定义 `Config.Logger` 后，内置 Logger 不再输出。
- `Capture` 的零值不采集 Header 和 Body；每个正文上限的 0 表示默认 4 KiB，负数无效。
- `NewSlogLogger` 默认使用 Info 级别。Handler 禁用该级别时，Start 返回 nil，本次也不采集 Header/Body。

## 替换为公司的日志实现

实现 `Logger.Start` 和返回的 `Session` 即可。也可以直接使用函数适配器，下面用 slog 演示如何自行组织字段；业务项目可以替换为自己的日志方法。

```go
companyLog := slog.Default()
config := &logging.Config{
    Capture: logging.CaptureOptions{
        RequestHeaders: true, ResponseHeaders: true,
        RequestBody: true, ResponseBody: true,
    },
    Logger: logging.LoggerFunc(func(ctx context.Context, info logging.CallInfo) logging.Session {
        // 每次 Start 只属于一个逻辑调用，闭包中的状态不会与其他调用混用。
        log := companyLog.With("track_id", info.TraceID, "request_id", info.RequestID)
        return logging.SessionFuncs{
            AttemptEnd: func(ctx context.Context, result logging.AttemptResult) {
                // 这里拿到该次尝试的错误，包括后来重试成功之前的失败。
                // Err 保留原始错误链；按业务白名单提取错误码，避免直接输出敏感错误文本。
                attrs := []any{
                    "attempt", result.Attempt,
                    "duration", result.Duration,
                    "phase", result.Phase,
                    "failed", result.Err != nil,
                    "request_headers", result.Request.Header,
                    "request_body", string(result.Request.Body.Data),
                    "request_body_omitted", result.Request.Body.OmittedReason,
                    "request_body_complete", result.Request.Body.Complete,
                    "request_body_truncated", result.Request.Body.Truncated,
                }
                if result.Response != nil {
                    attrs = append(attrs,
                        "status", result.Response.StatusCode,
                        "response_headers", result.Response.Header,
                        "response_body", string(result.Response.Body.Data),
                        "response_body_omitted", result.Response.Body.OmittedReason,
                        "response_body_complete", result.Response.Body.Complete,
                        "response_body_truncated", result.Response.Body.Truncated,
                    )
                }
                log.InfoContext(ctx, "outbound_attempt", attrs...)
            },
            Retry: func(ctx context.Context, plan logging.RetryEvent) {
                log.InfoContext(ctx, "outbound_retry",
                    "attempt", plan.Attempt, "next_attempt", plan.NextAttempt,
                    "delay", plan.Delay, "status", plan.StatusCode)
            },
            End: func(ctx context.Context, result logging.CallResult) {
                log.InfoContext(ctx, "outbound_complete",
                    "retries", result.RetryCount, "total_duration", result.Duration,
                    "limiter_wait_duration", result.LimiterWaitDuration,
                    "status", result.StatusCode, "failed", result.Err != nil)
            },
        }
    }),
}
_ = config // 赋给 tn.ApiParam.Logging。
```

如果只需要最终日志，只设置 `SessionFuncs.End` 即可。若需要在最后输出包含历次尝试的单条日志，可在本次 Session 内收集摘要，在 OnEnd 输出；库不会自动累计保存所有正文。

`TraceId` 对应业务链路，可映射为公司的 `track_id` 字段；同一链路可以包含多个请求。`RequestID` 由执行层为每次 Api 调用独立生成，同一调用的所有重试共用它。二者不会自动写入 HTTP 请求头，透传需由业务配置 Header。

## 生命周期和计数

| 回调 | 时机与信息 |
| --- | --- |
| Logger.Start | 参数检查前创建会话，收到 RequestID、TraceID、Caller、方法、脱敏 URL 和开始时间 |
| Session.OnAttemptEnd | 一次尝试结束、上传/响应流清理及超时判定后，收到该次耗时、错误与快照 |
| Session.OnRetry | 重试决定已确定、等待开始之前，收到失败原因、下一次尝试编号和计划等待时间 |
| Session.OnEnd | 本次调用资源清理后，收到最终错误、最近状态、实际次数和总耗时；最多一次 |

例如先失败再重试成功：

```text
Start
  OnAttemptEnd(attempt=1, err!=nil)
  OnRetry(attempt=1, next_attempt=2)
  OnAttemptEnd(attempt=2, err=nil)
OnEnd(attempts=2, retries=1)
```

`RetryCount = max(AttemptCount - 1, 0)`。OnRetry 表示计划，不代表下一次已经执行：等待中取消时，不增加实际重试次数。尝试从打开该次 Body 开始计数，所以 BodyFactory 打开失败也会有该次尝试的结束事件。参数错误、排队失败、公共正文准备失败或 ApiTo 前置检查失败时，尝试数为 0，仍有最终结束事件。

一次尝试覆盖 `http.Client.Do` 内的整条重定向链及响应读取/流式写入/业务校验。重定向每一跳和 Go Transport 的透明重试不单独计数。请求快照采于 Client.Do 之前，响应快照为该次最终收到的响应；它们用于业务诊断，不承诺还原线上全部报文。

`CallResult.StatusCode` 是各轮 Client.Do 最近一次返回的最终响应状态，未取得过响应才为 0；内部跳转的中间响应不单独统计。`RetryEvent.StatusCode` 和 `tn.RetryInfo.StatusCode` 只表示当前轮，没有响应就为 0。例如第一轮 503、第二轮网络失败时，第二轮 `AttemptResult.Response=nil`、重试信息中的当前轮状态为 0，而最终汇总状态仍为 503。整次调用是否成功应检查 `Err`。

总耗时从执行层入口开始，包含准备、配额排队、重试等待、各次尝试、同步日志回调及资源清理，不包含 OnEnd 自身的输出耗时。`Phase` 标识失败阶段，例如 parameters、admission、body_open、send、response_read、validation、retry_wait，便于区分失败位置。

共享限制默认关闭，只有显式配置 ApiParam.Limiter 时才会申请名额。最终 CallResult.LimiterWaitDuration 单独记录 Acquire 的耗时，包含检查开销，成功和失败均统计；未启用 Limiter 或申请前已经失败时为 0。该值属于总 Duration，各轮 AttemptResult.Duration 不包含它。内置 slog 的 http.call.end 输出 limiter_wait_duration 字段。申请失败时尝试数为 0、Phase 为 admission，错误属于调用执行结果，不进入日志异常回调 OnError。

## 采集与脱敏

请求/响应头与正文分别开关，采集先于脱敏。自定义 Logger 默认仍收到脱敏后的内容；更换日志输出格式不会自动关闭脱敏。

- 请求头是 Client.Do 之前的快照，不包含 CookieJar、重定向或 Transport 随后自动补充的内容。
- 请求正文只复制本次发送正常读取的字节，不调用额外的 BodyFactory/GetBody。若目标提前返回、上传没有读完，快照可能不完整。
- 响应正文只复制正常读取或 ApiTo 流式写出过程中经过的字节；不会为了日志额外排空响应。通常是 HTTP 层解压后的内容。
- `BodySnapshot.Complete` 表示确认正文已读完；`Truncated` 表示观察到超出日志上限的数据。二者独立，完整读取的大正文也可能被日志上限截断。
- `OmittedReason` 非空时，不能把空 Data 解释为空正文。常见值为 disabled、not_sent、incomplete、truncated、unsupported_content_type。
- 正文采集上限与实际上传/下载上限分开；不会因日志上限而截断真实 HTTP 数据。只有显式启用 Limiter 的共享容量限制时，采集上限才计入该预算；Limiter 为 nil 或只限制并发时跳过预算计算。

默认脱敏器只保留 URL origin，隐藏路径、查询参数和用户信息。Header 仅允许 Content-Type、Content-Length、Accept 的值，其余值统一替换。正文只输出完整且不超限的 JSON（含 +json）或 URL 表单，对内置敏感字段和 `SensitiveFields` 脱敏；非法、未知类型、截断或不完整正文整体省略。脱敏编码后的正文超过上限也会省略。

`Redactor` 可替换整个策略。`logging.RedactorFuncs{}` 明确表示原样通过，与 `Redactor: nil` 选择默认策略不同。自定义脱敏器可在已有默认策略基础上增加业务规则，也可按业务需求保留更多内容；应明确控制凭证、个人信息与正文的输出范围。

默认 slog 仅记录错误类型，不调用原始 `Err.Error()`。自定义会话拿到原始错误链，可以 `errors.Is/As` 分类；错误对象仅供只读，可能包含原始 URL、Header 或正文。

## 并发、数据所有权与异常

共享的 Logger、Redactor 和输出目标需要支持并发；每次 Start 返回独立 Session，同一 Session 的回调串行执行。配置在调用期间只读。Logger.Start 返回 nil 可以按业务条件跳过整个会话，执行层也跳过日志快照采集。

Header 和 Body.Data 在交给脱敏器和会话之前与 HTTP 数据隔离，可以保留或放入业务自己的异步日志队列。多个消费者需要相互隔离时可调用快照的 Clone。异步队列的容量、生命周期与丢弃策略由日志实现管理，库不会创建后台日志 goroutine。

回调同步执行，必须及时返回；慢回调会增加调用耗时并可能消耗总期限。Logger、Session、Redactor 的 panic 被隔离，通过 Config.OnError 报告 `*logging.CallbackError`，不覆盖 HTTP 错误或改变重试决定。脱敏器 panic 时省略该方向的数据，不回退打印原文。OnError 自身 panic 也被隔离；未配置时使用默认 slog 记录不含原始 panic 值的摘要。配置非法则在参数检查阶段返回错误。

可运行示例见 [logging_examples_test.go](../logging_examples_test.go) 和 [example_test.go](example_test.go)。
