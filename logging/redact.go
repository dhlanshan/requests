package logging

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// Redactor 处理隔离后的日志快照。实现必须能被并发调用，只修改传入的日志副本。
// 默认策略隐藏 URL 路径/查询、未知 Header，以及不适合安全解析的正文。
// 自定义策略可放宽规则；输出原始凭证、正文或地址的责任由选择该策略的调用方承担。
type Redactor interface {
	RedactRequest(RequestSnapshot) RequestSnapshot
	RedactResponse(ResponseSnapshot) ResponseSnapshot
}

// RedactorFuncs 用普通函数实现策略。未设置的方向原样通过；零值显式表示不脱敏。
// 它与 Config.Redactor=nil 不同：后者选择安全的 DefaultRedactor。
type RedactorFuncs struct {
	Request  func(RequestSnapshot) RequestSnapshot
	Response func(ResponseSnapshot) ResponseSnapshot
}

func (f RedactorFuncs) RedactRequest(r RequestSnapshot) RequestSnapshot {
	if f.Request != nil {
		return f.Request(r)
	}
	return r
}
func (f RedactorFuncs) RedactResponse(r ResponseSnapshot) ResponseSnapshot {
	if f.Response != nil {
		return f.Response(r)
	}
	return r
}

// DefaultRedactor 是保守的默认策略。SensitiveFields 追加业务字段名；不能关闭内置规则。
// 名称忽略大小写及 -、_、.，JSON 递归处理，表单按键处理；并非自然语言秘密识别器。
// 配置及 SensitiveFields 在复用期间应保持只读。
type DefaultRedactor struct{ SensitiveFields []string }

func (d DefaultRedactor) RedactRequest(r RequestSnapshot) RequestSnapshot {
	r.URL = origin(r.URL)
	r.Header = safeHeaders(r.Header)
	r.Body = d.body(r.Body, r.ContentType)
	return r
}

func (d DefaultRedactor) RedactResponse(r ResponseSnapshot) ResponseSnapshot {
	if r.URL != "" {
		r.URL = origin(r.URL)
	}
	r.Header = safeHeaders(r.Header)
	r.Body = d.body(r.Body, r.ContentType)
	return r
}

func origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "[omitted]"
	}
	return u.Scheme + "://" + u.Host
}

func safeHeaders(header http.Header) http.Header {
	if header == nil {
		return nil
	}
	result := make(http.Header, len(header))
	for key, values := range header {
		switch http.CanonicalHeaderKey(key) {
		case "Content-Type", "Content-Length", "Accept":
			copyValues := make([]string, len(values))
			for i, value := range values {
				copyValues[i] = clip(value)
			}
			result[key] = copyValues
		default:
			result[key] = []string{"[REDACTED]"}
		}
	}
	return result
}

func sensitive(key string, extra []string) bool {
	normalize := func(s string) string {
		return strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(s))
	}
	key = normalize(key)
	for _, token := range []string{"authorization", "cookie", "password", "passwd", "secret", "token", "apikey", "credential", "session", "privatekey"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	for _, field := range extra {
		if key == normalize(field) {
			return true
		}
	}
	return false
}

func (d DefaultRedactor) redactJSON(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if sensitive(key, d.SensitiveFields) {
				v[key] = "[REDACTED]"
			} else {
				d.redactJSON(child)
			}
		}
	case []any:
		for _, child := range v {
			d.redactJSON(child)
		}
	}
}

func (d DefaultRedactor) body(body BodySnapshot, contentType string) BodySnapshot {
	omit := func(reason string) BodySnapshot { body.Data = nil; body.OmittedReason = reason; return body }
	if body.OmittedReason != "" {
		return omit(body.OmittedReason)
	}
	if body.Truncated {
		return omit("truncated")
	}
	if !body.Complete {
		return omit("incomplete")
	}
	if len(body.Data) == 0 {
		return body
	}
	ct, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return omit("unsupported_content_type")
	}
	switch {
	case ct == "application/json" || strings.HasSuffix(ct, "+json"):
		decoder := json.NewDecoder(bytes.NewReader(body.Data))
		decoder.UseNumber()
		var value, extra any
		if decoder.Decode(&value) != nil || decoder.Decode(&extra) != io.EOF {
			return omit("invalid_json")
		}
		d.redactJSON(value)
		body.Data, err = json.Marshal(value)
	case ct == "application/x-www-form-urlencoded":
		var values url.Values
		values, err = url.ParseQuery(string(body.Data))
		if err != nil {
			return omit("invalid_form")
		}
		for key := range values {
			if sensitive(key, d.SensitiveFields) {
				values[key] = []string{"[REDACTED]"}
			}
		}
		body.Data = []byte(values.Encode())
	default:
		return omit("unsupported_content_type")
	}
	if err != nil {
		return omit("redaction_failed")
	}
	return body
}

func clip(value string) string {
	if len(value) > 512 {
		return value[:512] + "…"
	}
	return value
}
