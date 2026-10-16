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
			used := false
			plan.open = func(context.Context) (io.ReadCloser, int64, error) {
				if used {
					return nil, 0, tn.ErrBodyNotReplayable
				}
				used = true
				return body, req.ContentLength, nil
			}
		}
	}
	plan = withDeclaredLength(plan, p)
	if !retry || plan.replayable {
		return plan, nil
	}
	r, _, err := plan.open(ctx)
	if err != nil {
		if !utils.IsNil(r) {
			_ = r.Close()
		}
		return bodyPlan{}, fmt.Errorf("open request body: %w", err)
	}
	if utils.IsNil(r) {
		return bodyPlan{}, errors.New("body source returned nil body")
	}
	data, err := lz.ReadBounded(ctx, r, p.MaxReplayBytes)
	if errors.Is(err, tn.ErrResponseTooLarge) {
		return bodyPlan{}, tn.ErrRequestBodyTooLarge
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
	open, replayable := plan.open, plan.replayable
	verifiedEmpty := false
	if declared == 0 {
		// 先在单次 Context 内验证；空的一次性流可直接重放 NoBody，无需进入重试缓存。
		plan.replayable = true
	}
	plan.open = func(ctx context.Context) (io.ReadCloser, int64, error) {
		if verifiedEmpty {
			return http.NoBody, 0, nil
		}
		body, length, err := open(ctx)
		if err != nil {
			return body, length, err
		}
		if length < -1 {
			if !utils.IsNil(body) {
				_ = body.Close()
			}
			return nil, 0, errors.New("body factory returned invalid length")
		}
		if length >= 0 && length != declared {
			if !utils.IsNil(body) {
				_ = body.Close()
			}
			return nil, 0, errors.New("Content-Length does not match Body")
		}
		if declared == 0 && !utils.IsNil(body) {
			// net/http 将非 nil Body 的长度 0 视为未知长度；必须先确认 EOF，再转为 NoBody。
			if body != http.NoBody {
				if err := checkEmptyBody(ctx, body); err != nil {
					return nil, 0, err
				}
			}
			if !replayable {
				verifiedEmpty = true
			}
			return http.NoBody, 0, nil
		}
		return body, declared, nil
	}
	return plan
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
	if err := ctx.Err(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	var probe [1]byte
	n, readErr := io.ReadFull(body, probe[:])
	if err := ctx.Err(); err != nil {
		return err
	}
	if n != 0 {
		return errors.New("Content-Length does not match Body")
	}
	if readErr != io.EOF {
		return readErr
	}
	return nil
}

func (plan bodyPlan) request(ctx context.Context, template *http.Request) (*http.Request, error) {
	body, length, err := plan.open(ctx)
	if err != nil {
		if !utils.IsNil(body) {
			_ = body.Close()
		}
		return nil, fmt.Errorf("open request body: %w", err)
	}
	if utils.IsNil(body) {
		return nil, errors.New("body factory returned nil body")
	}
	if err := ctx.Err(); err != nil {
		_ = body.Close()
		return nil, err
	}
	if length < -1 {
		_ = body.Close()
		return nil, errors.New("body factory returned invalid length")
	}
	req := template.Clone(ctx)
	req.Body = lz.Guard(body)
	req.ContentLength = length
	if body == http.NoBody {
		req.Body = http.NoBody
	}
	if plan.replayable {
		req.GetBody = func() (io.ReadCloser, error) {
			r, n, err := plan.open(ctx)
			if err != nil {
				if !utils.IsNil(r) {
					_ = r.Close()
				}
				return nil, err
			}
			if utils.IsNil(r) {
				return nil, errors.New("body factory returned nil body")
			}
			if n != length {
				_ = r.Close()
				return nil, errors.New("body factory changed content length")
			}
			if r == http.NoBody {
				return http.NoBody, nil
			}
			return lz.Guard(r), nil
		}
	} else {
		req.GetBody = nil
	}
	return req, nil
}
