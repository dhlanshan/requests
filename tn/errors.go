package tn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

var (
	ErrResponseTooLarge    = errors.New("response body exceeds limit")
	ErrRequestBodyTooLarge = errors.New("request replay buffer exceeds limit; use BodyFactory for large streams")
	ErrBodyNotReplayable   = errors.New("request body cannot be replayed")
	ErrValidation          = errors.New("response data verification failed")
	ErrTargetBlocked       = errors.New("request target is blocked by policy")
	ErrClientConfig        = errors.New("invalid or conflicting client configuration")
	ErrAdmissionTooLarge   = errors.New("request exceeds the configured buffer budget")
	ErrResponseWrite       = errors.New("writing response failed")
	ErrPermanent           = errors.New("request failure must not be retried")
)

// HTTPError 保留有界错误响应，Error 不输出可能包含凭证的 URL 和 Body。
type HTTPError struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Truncated  bool
}

func (e *HTTPError) Error() string { return fmt.Sprintf("http status code: %d", e.StatusCode) }

// RequestError 的展示文本不拼接 URL 或底层错误文本；Unwrap 保留诊断和 errors.Is/As 能力。
// 业务显式展开原始错误时仍需自行处理敏感信息。
type RequestError struct{ Err error }

func (e *RequestError) Error() string {
	switch {
	case errors.Is(e.Err, context.Canceled):
		return "http request canceled"
	case errors.Is(e.Err, context.DeadlineExceeded):
		return "http request timed out"
	case errors.Is(e.Err, ErrTargetBlocked):
		return "http request target blocked"
	case errors.Is(e.Err, ErrClientConfig):
		return "http client configuration error"
	default:
		return "http request failed"
	}
}
func (e *RequestError) Unwrap() error { return e.Err }

func (e *RequestError) Timeout() bool {
	var network net.Error
	return errors.Is(e.Err, context.DeadlineExceeded) || (errors.As(e.Err, &network) && network.Timeout())
}

func (e *RequestError) Temporary() bool {
	var network net.Error
	return errors.As(e.Err, &network) && network.Temporary()
}

type permanentError struct{ err error }

func (e *permanentError) Error() string        { return "non-retryable request failure" }
func (e *permanentError) Unwrap() error        { return e.err }
func (e *permanentError) Is(target error) bool { return target == ErrPermanent }

// NoRetry 标记永久业务失败，保留原错误链；nil 输入仍返回 nil。
func NoRetry(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}
