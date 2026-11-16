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
// 每份正文开始遍历前规范化一次自定义规则；不改写配置，配置及 SensitiveFields 在复用期间应保持只读。
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

// 替换器只构造一次，初始化后由标准库保证并发使用安全；不按 JSON 字段重复构造。
var fieldNameReplacer = strings.NewReplacer("-", "", "_", "", ".", "")

func normalizeFieldName(name string) string {
	return strings.ToLower(fieldNameReplacer.Replace(name))
}

// sensitiveFields 是一份正文专用的只读规则集合，不写回 DefaultRedactor，也不使用全局规则缓存。
// 自定义字段在开始遍历正文前规范化一次，嵌套对象、数组和表单键共用该集合。
type sensitiveFields map[string]struct{}

func newSensitiveFields(extra []string) sensitiveFields {
	if len(extra) == 0 {
		return nil
	}
	fields := make(sensitiveFields, len(extra))
	for _, name := range extra {
		fields[normalizeFieldName(name)] = struct{}{}
	}
	return fields
}

func (fields sensitiveFields) matches(key string) bool {
	key = normalizeFieldName(key)
	for _, token := range []string{"authorization", "cookie", "password", "passwd", "secret", "token", "apikey", "credential", "session", "privatekey"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	_, found := fields[key]
	return found
}

func redactJSON(value any, fields sensitiveFields) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if fields.matches(key) {
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
		redactJSON(value, newSensitiveFields(d.SensitiveFields))
		body.Data, err = json.Marshal(value)
	case ct == "application/x-www-form-urlencoded":
		var values url.Values
		values, err = url.ParseQuery(string(body.Data))
		if err != nil {
			return omit("invalid_form")
		}
		fields := newSensitiveFields(d.SensitiveFields)
		for key := range values {
			if fields.matches(key) {
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
