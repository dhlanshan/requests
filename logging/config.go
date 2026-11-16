package logging

import (
	"context"
	"errors"
	"math"
	"reflect"
)

// DefaultBodyLimit 是开启正文采集但没有指定上限时的默认值，每个方向为 4 KiB。
const DefaultBodyLimit int64 = 4 << 10

// CaptureOptions 控制执行层采集哪些内容。零值仅记录基础元数据。
// 两个上限均以字节为单位，0 使用 DefaultBodyLimit，负数无效。
// 上限限制采集缓存，不限制实际 HTTP 正文大小；日志扩展不能依靠此设置提前读完整个流。
type CaptureOptions struct {
	RequestHeaders  bool // 复制 Client.Do 之前的请求头；不包含随后自动加入的 Cookie 等字段。
	ResponseHeaders bool // 复制本次尝试最终响应的头，保留同名头的多个值。
	RequestBody     bool // 只复制发送过程实际读取到的正文，不额外打开 BodyFactory/GetBody。
	ResponseBody    bool // 只复制正常读取/流式写入过程中经过的正文，成功和失败响应均适用。

	RequestBodyLimit  int64
	ResponseBodyLimit int64
}

// Config 描述一份可复用的只读日志配置，调用期间不能并发修改。
// nil *Config 关闭日志；非 nil Config 的 nil Logger 选择 NewSlogLogger(nil)。
// Redactor 为 nil 时使用 DefaultRedactor。OnError 接收日志回调/脱敏器的 panic，
// 不改变 HTTP 返回值或重试策略。OnError 自己的 panic 也会被隔离。
// 不设置 OnError 时，扩展异常通过默认 slog 记录安全摘要，不输出 panic 的原始内容。
type Config struct {
	Logger   Logger
	Capture  CaptureOptions
	Redactor Redactor
	OnError  func(context.Context, error)
}

// Validate 检查可在请求发送前发现的配置错误，不执行任何用户回调。
func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	for _, limit := range []int64{c.Capture.RequestBodyLimit, c.Capture.ResponseBodyLimit} {
		if limit < 0 || limit > math.MaxInt64/2 {
			return errors.New("logging body limit is out of range")
		}
	}
	if c.Logger != nil && nilValue(c.Logger) {
		return errors.New("nil logging logger")
	}
	if c.Redactor != nil && nilValue(c.Redactor) {
		return errors.New("nil logging redactor")
	}
	return nil
}

func nilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Func, reflect.Slice, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}

func (c CaptureOptions) defaults() CaptureOptions {
	if c.RequestBodyLimit == 0 {
		c.RequestBodyLimit = DefaultBodyLimit
	}
	if c.ResponseBodyLimit == 0 {
		c.ResponseBodyLimit = DefaultBodyLimit
	}
	return c
}
