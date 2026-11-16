// Package runner 在 http.Client 之上统一执行超时、重试、校验和日志策略。
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dhlanshan/requests/internal/lz"
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/tn"
)

// NewRequest 正确处理 Host 和 Content-Length；它们不是普通请求头。
func NewRequest(ctx context.Context, p tn.ApiParam) (*http.Request, error) {
	reader := p.Body
	if _, ok := reader.(tn.BodySource); ok {
		reader = nil
	}
	req, err := http.NewRequestWithContext(ctx, p.Method, p.Url, reader)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	for k, v := range p.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Host":
			req.Host = v
		case "Content-Length":
			n, parseErr := strconv.ParseInt(v, 10, 64)
			if parseErr != nil || n < 0 {
				return nil, errors.New("invalid Content-Length")
			}
			if req.GetBody != nil && n != req.ContentLength {
				return nil, errors.New("Content-Length does not match Body")
			}
			req.ContentLength = n
		case "Transfer-Encoding":
			return nil, errors.New("Transfer-Encoding is managed by net/http")
		default:
			req.Header.Set(k, v)
		}
	}
	stripBlankIdempotencyKeys(req.Header)
	if req.URL.RawQuery != "" || len(p.Params) != 0 {
		query, parseErr := url.ParseQuery(req.URL.RawQuery)
		if parseErr != nil {
			return nil, errors.New("invalid URL query encoding")
		}
		for k, v := range p.Params {
			query.Add(k, v)
		}
		if len(p.Params) != 0 {
			req.URL.RawQuery = query.Encode()
		}
	}
	return req, nil
}

// stripBlankIdempotencyKeys 删除没有业务标识的幂等键，避免标准 Transport 仅因头存在就重发 POST。
// 同样处理重定向回调写入的 nil、空切片、多值和非规范大小写头名；有效值保留原文及顺序。
// 不修改原值切片，避免回调将外部配置切片赋给 Header 后被此处改写。
func stripBlankIdempotencyKeys(header http.Header) {
	for name, values := range header {
		switch http.CanonicalHeaderKey(name) {
		case "Idempotency-Key", "X-Idempotency-Key":
		default:
			continue
		}
		valid := 0
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				valid++
			}
		}
		switch {
		case valid == 0:
			// Header[name]=nil 或 []string{} 仍然算头存在，必须删除整个键。
			delete(header, name)
		case valid != len(values):
			filtered := make([]string, 0, valid)
			for _, value := range values {
				if strings.TrimSpace(value) != "" {
					filtered = append(filtered, value)
				}
			}
			header[name] = filtered
		}
	}
}

type bodyPlan struct {
	open       tn.BodyFactory
	replayable bool
}

// prepareBody 对可重建流直接重开；只在需要重试时缓存未知流，且设硬上限。
// 不为日志读取或缓存上传 Body。
func prepareBody(ctx context.Context, req *http.Request, p tn.ApiParam, owner io.ReadCloser, retry bool) (bodyPlan, error) {
	var plan bodyPlan
	switch source := p.Body.(type) {
	case tn.BodySource:
		plan = bodyPlan{source.NewBody, source.Replayable()}
	default:
		switch {
		case p.BodyFactory != nil:
			plan = bodyPlan{p.BodyFactory, true}
		case req.Body == nil || req.Body == http.NoBody:
			plan = bodyPlan{func(context.Context) (io.ReadCloser, int64, error) { return http.NoBody, 0, nil }, true}
		case req.GetBody != nil:
			plan = bodyPlan{func(context.Context) (io.ReadCloser, int64, error) {
				r, err := req.GetBody()
				return r, req.ContentLength, err
			}, true}
		default:
			body := req.Body
			if owner != nil {
				body = owner
			}
			length := req.ContentLength
			if length == 0 {
				// 普通非 nil Reader 的 0 在 net/http 中表示未知长度，不是工厂承诺的空正文。
				// 在内部使用 -1，防止统一空正文处理误拒绝正常的流式上传。
				length = -1
			}
			used := false
			plan.open = func(context.Context) (io.ReadCloser, int64, error) {
				if used {
					return nil, 0, tn.ErrBodyNotReplayable
				}
				used = true
				return body, length, nil
			}
		}
	}
	plan = withDeclaredLength(plan, p)
	if !retry || plan.replayable {
		return plan, nil
	}
	r, _, err := plan.openBody(ctx)
	if err != nil {
		return bodyPlan{}, fmt.Errorf("open request body: %w", err)
	}
	data, err := lz.ReadBounded(ctx, r, p.MaxReplayBytes)
	if errors.Is(err, tn.ErrResponseTooLarge) {
		return bodyPlan{}, utils.MergeContextError(tn.ErrRequestBodyTooLarge, ctx.Err())
	}
	if err != nil {
		return bodyPlan{}, fmt.Errorf("prepare request body: %w", err)
	}
	return withDeclaredLength(bodyPlan{func(context.Context) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	}, true}, p), nil
}

func withDeclaredLength(plan bodyPlan, p tn.ApiParam) bodyPlan {
	declared, found := declaredContentLength(p.Header)
	if !found {
		return plan
	}
	original := plan
	verifiedEmpty := false
	if declared == 0 {
		// 先在单次 Context 内验证；空的一次性流可直接重放 NoBody，无需进入重试缓存。
		plan.replayable = true
	}
	plan.open = func(ctx context.Context) (io.ReadCloser, int64, error) {
		if verifiedEmpty {
			return http.NoBody, 0, nil
		}
		body, length, err := original.openBody(ctx)
		if err != nil {
			return body, length, err
		}
		if length >= 0 && length != declared {
			_ = body.Close()
			return nil, 0, errors.New("Content-Length does not match Body")
		}
		if declared == 0 {
			// net/http 将非 nil Body 的长度 0 视为未知长度；必须先确认 EOF，再转为 NoBody。
			if body != http.NoBody {
				if err := checkEmptyBody(ctx, body); err != nil {
					return nil, 0, err
				}
			}
			if !original.replayable {
				verifiedEmpty = true
			}
			return http.NoBody, 0, nil
		}
		return body, declared, nil
	}
	return plan
}

// openBody 为首次打开、重试缓存和 GetBody 重开统一所有权、长度及空正文语义。
// 0 是工厂准确声明的空正文；-1 是未知长度，不能为了判断是否为空而预读它。
// 失败时关闭已经接管的流，成功时交给当前请求或有界读取器负责关闭。
func (plan bodyPlan) openBody(ctx context.Context) (io.ReadCloser, int64, error) {
	body, length, err := plan.open(ctx)
	if !utils.IsNil(body) && body != http.NoBody {
		body = lz.Guard(body)
	}
	err = utils.MergeContextError(err, ctx.Err())
	if err == nil {
		switch {
		case utils.IsNil(body):
			err = errors.New("body factory returned nil body")
		case length < -1:
			err = errors.New("body factory returned invalid length")
		case body == http.NoBody && length > 0:
			err = errors.New("body factory length does not match NoBody")
		}
	}
	if err != nil {
		if !utils.IsNil(body) {
			_ = body.Close()
		}
		return nil, 0, err
	}
	if body == http.NoBody {
		return http.NoBody, 0, nil
	}
	if length == 0 {
		// checkEmptyBody 在 EOF、非空、错误、取消和 panic 路径均关闭原流。
		if err := utils.MergeContextError(checkEmptyBody(ctx, body), ctx.Err()); err != nil {
			return nil, 0, err
		}
		return http.NoBody, 0, nil
	}
	return body, length, nil
}

func declaredContentLength(header map[string]string) (int64, bool) {
	for key, value := range header {
		if http.CanonicalHeaderKey(key) == "Content-Length" {
			length, _ := strconv.ParseInt(value, 10, 64) // NewRequest 已校验格式。
			return length, true
		}
	}
	return 0, false
}

// checkEmptyBody 最多探测一个字节，受本次尝试的 Context 控制，并在所有路径关闭原流。
func checkEmptyBody(ctx context.Context, body io.ReadCloser) (err error) {
	body = lz.Guard(body)
	defer func() {
		if closeErr := body.Close(); err == nil {
			err = closeErr
		}
	}()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	var probe [1]byte
	n, readErr := io.ReadFull(body, probe[:])
	if n != 0 {
		return utils.MergeContextError(errors.New("Content-Length does not match Body"), ctx.Err())
	}
	if readErr != io.EOF {
		return utils.MergeContextError(readErr, ctx.Err())
	}
	return ctx.Err()
}

func (plan bodyPlan) request(ctx context.Context, template *http.Request) (*http.Request, error) {
	body, length, err := plan.openBody(ctx)
	if err != nil {
		return nil, fmt.Errorf("open request body: %w", err)
	}
	req := template.Clone(ctx)
	req.Body = body
	req.ContentLength = length
	if plan.replayable {
		req.GetBody = func() (io.ReadCloser, error) {
			r, n, err := plan.openBody(ctx)
			if err != nil {
				return nil, err
			}
			if n != length {
				_ = r.Close()
				return nil, errors.New("body factory changed content length")
			}
			return r, nil
		}
	} else {
		req.GetBody = nil
	}
	return req, nil
}
