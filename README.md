# Custom HTTP Request Client

![Go](https://img.shields.io/badge/Go-%3E%3D%201.16-blue.svg)
![MIT License](https://img.shields.io/badge/License-MIT-green.svg)

该项目是一个自定义 HTTP 请求发送工具，支持多种 HTTP 请求方法（如 GET、POST 等），并提供请求重试、响应验证等功能。它封装了 HTTP 请求的常见操作，方便在 Go 项目中进行调用。

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

## 安装
