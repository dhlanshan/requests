# Custom HTTP Request Client

![Go](https://img.shields.io/badge/Go-%3E%3D%201.16-blue.svg)
![MIT License](https://img.shields.io/badge/License-MIT-green.svg)

一个自定义 HTTP 请求发送工具，支持多种 HTTP 请求方法（如 GET、POST 等），并提供请求重试、响应验证等功能。它封装了 HTTP 请求的常见操作，方便在 Go 项目中进行调用。

## 特性

- 🎯 **多种 HTTP 请求方法支持**：支持常见的请求方法：`GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD`, `OPTIONS`, `TRACE`, `CONNECT`。
- 🔄 **请求重试机制**：可设置最大重试次数以及重试间隔，确保请求在遇到临时问题时能够自动重试。
- 🛠️ **响应验证**：支持自定义响应验证，用户可以提供一个验证器函数来判断响应是否符合预期。
- 🔒 **灵活的客户端配置**：允许配置 HTTP 客户端的各类选项，如超时、重定向策略、Cookie 管理等。
- 📄 **日志功能**：支持打印请求和响应日志，便于调试和监控。

## 功能概述

### 1. 支持多种 HTTP 请求方法

该库支持以下 HTTP 请求方法，你可以通过配置 `ApiParam` 中的 `Method` 字段来选择需要的请求类型：

- `GET`
- `POST`
- `PUT`
- `PATCH`
- `DELETE`
- `HEAD`
- `OPTIONS`
- `TRACE`
- `CONNECT`

### 2. 请求重试机制

- `Retry`：指定请求的最大重试次数。
- `RetryInterval`：设置重试的间隔时间（秒）。

### 3. 响应验证

通过提供自定义 `Validator` 函数，你可以根据响应内容来验证请求结果。例如，验证响应体中的某些字段值是否符合预期。

### 4. HTTP 客户端配置

- `Timeout`：设置请求的超时时间。
- `Transport`：允许你提供一个自定义的 `RoundTripper`，用于修改 HTTP 请求过程。
- `CheckRedirect`：设置重定向策略。
- `Jar`：自定义 `CookieJar`，用于管理 cookies。

### 5. 请求与响应日志

你可以通过 `EchoReq` 和 `EchoRes` 来启用请求和响应日志输出，以便于开发过程中的调试。

## 使用示例
## API 参数说明

`ApiParam` 是发送请求时所需要的配置项，主要包括以下字段：

| 字段            | 类型                    | 说明                                                         |
|-----------------|-------------------------|--------------------------------------------------------------|
| `Url`           | `string`                | 请求的 URL                                                   |
| `Method`        | `string`                | 请求的方法，如 "GET"、"POST" 等                              |
| `Header`        | `map[string]string`     | 请求头                                                       |
| `Params`        | `map[string]string`     | URL 查询参数                                                 |
| `Data`          | `[]byte`                | 请求体数据                                                   |
| `Retry`         | `int`                   | 重试次数                                                     |
| `RetryInterval` | `time.Duration`         | 重试间隔，单位秒                                             |
| `EnableValid`   | `bool`                  | 是否启用响应验证                                           |
| `Validator`     | `Validator`             | 自定义验证函数，返回布尔值，表示响应是否合法               |
| `Timeout`       | `time.Duration`         | 请求超时时间                                                 |
| `SpaceName`     | `string`                | 客户端空间名称，用于缓存客户端                              |
| `Transport`     | `http.RoundTripper`     | 自定义 Transport，支持自定义请求过程                       |
| `CheckRedirect` | `CheckRedirectFunc`     | 自定义重定向检查函数                                         |
| `Jar`           | `http.CookieJar`        | 自定义 CookieJar                                            |
| `EchoReq`       | `bool`                  | 是否打印请求日志                                            |
| `EchoRes`       | `bool`                  | 是否打印响应日志                                            |


## 自定义验证器

你可以通过 `Validator` 字段自定义响应验证器。验证器是一个函数，接受响应体和响应头作为参数，返回一个布尔值，表示验证是否通过。

### 自定义验证器示例

```go
type Abc struct {
    Code int    `json:"code"`
    Msg  string `json:"msg"`
    Data struct {
        AwardNoticeTime string `json:"award_notice_time"`
        AwardRule       []any  `json:"award_rule"`
    } `json:"data"`
}

func AAValidator(respBody []byte, respHeader http.Header) bool {
    busStatus := true
    var rp Abc
    err := json.Unmarshal(respBody, &rp)
    if err != nil {
        return false
    }
    if rp.Code != 0 || rp.Msg != "ok" {
        busStatus = false
    }
    fmt.Println(respHeader.Get("date"))

    return busStatus
}
```