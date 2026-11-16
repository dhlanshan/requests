package logging

import (
	"context"
	"fmt"
	"log/slog"
)

// SlogLogger 是内置输出适配器，只使用标准库。Logger 为 nil 时使用 slog.Default()。
// Level 的零值是 Info；可在创建后、并发使用前调整。所有生命周期事件使用同一 Level，
// 错误分类通过 phase/error_type 提供，避免把普通 4xx 一律解释为日志系统故障。
type SlogLogger struct {
	Logger *slog.Logger
	Level  slog.Level
}

// NewSlogLogger 允许调用方控制 Handler、输出位置和文本/JSON 格式。
// 自定义整个日志协议时，实现 Logger，而不是依赖本适配器的固定字段名。
func NewSlogLogger(logger *slog.Logger) *SlogLogger {
	return &SlogLogger{Logger: logger, Level: slog.LevelInfo}
}

func (l *SlogLogger) Start(ctx context.Context, info CallInfo) Session {
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if !logger.Enabled(ctx, l.Level) {
		return nil
	}
	session := &slogSession{logger: logger, level: l.Level, info: info}
	session.write(ctx, "http.call.start", nil)
	return session
}

type slogSession struct {
	logger *slog.Logger
	level  slog.Level
	info   CallInfo
}

func (s *slogSession) write(ctx context.Context, message string, attrs []any) {
	common := []any{"request_id", s.info.RequestID, "trace_id", clip(s.info.TraceID), "caller", clip(s.info.Caller), "method", s.info.Method, "url", s.info.URL}
	s.logger.Log(ctx, s.level, message, append(common, attrs...)...)
}

func (s *slogSession) OnAttemptEnd(ctx context.Context, r AttemptResult) {
	attrs := []any{"attempt", r.Attempt, "duration", r.Duration, "phase", r.Phase,
		"request", slog.GroupValue(slog.String("method", r.Request.Method), slog.String("url", r.Request.URL), slog.String("host", r.Request.Host), slog.Any("headers", r.Request.Header), slog.Any("body", slogBody(r.Request.Body)))}
	if r.Response != nil {
		attrs = append(attrs, "response", slog.GroupValue(slog.Int("status", r.Response.StatusCode), slog.String("url", r.Response.URL), slog.Any("headers", r.Response.Header), slog.Any("body", slogBody(r.Response.Body))))
	}
	if r.Err != nil {
		attrs = append(attrs, "error_type", fmt.Sprintf("%T", r.Err))
	}
	s.write(ctx, "http.attempt.end", attrs)
}

func (s *slogSession) OnRetry(ctx context.Context, r RetryEvent) {
	s.write(ctx, "http.retry", []any{"attempt", r.Attempt, "next_attempt", r.NextAttempt, "delay", r.Delay, "status", r.StatusCode, "phase", r.Phase, "error_type", fmt.Sprintf("%T", r.Err)})
}

func (s *slogSession) OnEnd(ctx context.Context, r CallResult) {
	attrs := []any{"attempts", r.AttemptCount, "retries", r.RetryCount, "duration", r.Duration,
		"limiter_wait_duration", r.LimiterWaitDuration, "status", r.StatusCode, "phase", r.Phase}
	if r.Err != nil {
		attrs = append(attrs, "error_type", fmt.Sprintf("%T", r.Err))
	}
	s.write(ctx, "http.call.end", attrs)
}

// slogBody 把正文输出为可读文本，并把省略原因与完整性标记保留为结构化字段。
// 不直接序列化 []byte，避免 JSON Handler 将正文转成难以阅读的 base64 字符串。
type slogBody BodySnapshot

func (b slogBody) LogValue() slog.Value {
	data := string(b.Data)
	if b.OmittedReason != "" {
		data = "[omitted]"
	}
	return slog.GroupValue(slog.String("data", data), slog.Bool("complete", b.Complete), slog.Bool("truncated", b.Truncated), slog.String("omitted_reason", b.OmittedReason))
}
