package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dhlanshan/requests/tn"
)

func logEnabled(ctx context.Context, p tn.ApiParam) bool {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return logger.Enabled(ctx, slog.LevelInfo)
}

// captureBody 随发送过程捕获前缀，不预读、不改变流式上传或 GetBody。
type captureBody struct {
	io.ReadCloser
	mu       sync.Mutex
	data     []byte
	limit    int
	complete bool
	read     int64
}

func (c *captureBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.read += int64(n)
	if remaining := c.limit + 1 - len(c.data); remaining > 0 {
		if remaining > n {
			remaining = n
		}
		c.data = append(c.data, p[:remaining]...)
	}
	if err == io.EOF {
		c.complete = true
	}
	return n, err
}

func (c *captureBody) markLength(length int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.read == length {
		c.complete = true
	}
}

func (c *captureBody) preview(contentType string, p tn.ApiParam) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return preview(c.data, contentType, c.complete, p)
}

func sensitive(key string, extra []string) bool {
	key = strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(key))
	for _, token := range []string{"authorization", "cookie", "password", "passwd", "secret", "token", "apikey", "credential", "session", "privatekey"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	for _, field := range extra {
		if key == strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(field)) {
			return true
		}
	}
	return false
}

func redactJSON(value any, fields []string) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if sensitive(key, fields) {
				v[key] = "[REDACTED]"
			} else {
				redactJSON(child, fields)
			}
		}
	case []any:
		for _, child := range v {
			redactJSON(child, fields)
		}
	}
}

// preview 仅解析完整且较小的 JSON/表单；截断片段无法可靠脱敏，因此整体省略。
func preview(data []byte, contentType string, complete bool, p tn.ApiParam) string {
	if !p.LogBody || !complete || len(data) > p.LogBodyLimit {
		return "[omitted]"
	}
	ct, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "[omitted]"
	}
	var result []byte
	switch {
	case ct == "application/json" || strings.HasSuffix(ct, "+json"):
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			return "[omitted]"
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return "[omitted]"
		}
		redactJSON(value, p.SensitiveFields)
		result, err = json.Marshal(value)
	case ct == "application/x-www-form-urlencoded":
		values, parseErr := url.ParseQuery(string(data))
		if parseErr != nil {
			return "[omitted]"
		}
		for k := range values {
			if sensitive(k, p.SensitiveFields) {
				values[k] = []string{"[REDACTED]"}
			}
		}
		result = []byte(values.Encode())
	default:
		return "[omitted]"
	}
	if err != nil || len(result) > p.LogBodyLimit {
		return "[omitted]"
	}
	return string(result)
}

// Header 使用白名单，未知自定义头也不会意外暴露认证信息。
func safeHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key := range headers {
		switch http.CanonicalHeaderKey(key) {
		case "Content-Type", "Content-Length", "Accept":
			result[key] = clip(headers.Get(key))
		default:
			result[key] = "[REDACTED]"
		}
	}
	return result
}

func clip(value string) string {
	if len(value) > 512 {
		return value[:512] + "…"
	}
	return value
}

func logResult(p tn.ApiParam, req *http.Request, id string, started time.Time, attempts, status int, body []byte, header http.Header, captured *captureBody, err error) {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ctx := req.Context()
	if !logger.Enabled(ctx, slog.LevelInfo) {
		return
	}
	common := []any{"request_id", id, "trace_id", clip(p.TraceId), "caller", clip(p.Caller), "method", req.Method,
		// 不记录 URL 路径/查询：业务经常把令牌放在这两处。
		"origin", req.URL.Scheme + "://" + req.URL.Host}
	if p.EchoReq {
		args := append(append([]any(nil), common...), "headers", safeHeaders(req.Header))
		if p.LogBody {
			value := "[omitted]"
			if captured != nil {
				value = captured.preview(req.Header.Get("Content-Type"), p)
			}
			args = append(args, "body", value)
		}
		logger.InfoContext(ctx, "http.request", args...)
	}
	if p.EchoRes {
		args := append(append([]any(nil), common...), "status", status, "attempts", attempts, "duration", time.Since(started), "headers", safeHeaders(header))
		if p.LogBody {
			args = append(args, "body", preview(body, header.Get("Content-Type"), err == nil, p))
		}
		// 原始错误可能嵌入带密码的 URL 或 Body，日志只记录错误类型。
		if err != nil {
			args = append(args, "error_type", fmt.Sprintf("%T", err))
		}
		logger.InfoContext(ctx, "http.response", args...)
	}
}
