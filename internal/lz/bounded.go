package lz

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"

	"github.com/dhlanshan/requests/tn"
)

const DefaultResponseLimit int64 = 16 << 20

// OnceReadCloser 允许超时回调与正常清理同时关闭流，但底层只关闭一次。
// 与 net/http 的 Body 约定一致，底层 Read 必须能与 Close 并发执行。
type OnceReadCloser struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (r *OnceReadCloser) Close() error {
	r.once.Do(func() { r.err = r.ReadCloser.Close() })
	return r.err
}

func Guard(r io.ReadCloser) *OnceReadCloser {
	if guarded, ok := r.(*OnceReadCloser); ok {
		return guarded
	}
	return &OnceReadCloser{ReadCloser: r}
}

// ReadBounded 多读一个字节以区分“刚好达到上限”和超限。
// 保留部分数据及底层读取错误；不会为了复用连接而无限排空响应。
func ReadBounded(ctx context.Context, r io.ReadCloser, limit int64) (data []byte, err error) {
	guarded := Guard(r)
	defer func() {
		if closeErr := guarded.Close(); err == nil {
			err = closeErr
		}
	}()
	if limit <= 0 || limit == math.MaxInt64 {
		return nil, errors.New("invalid body limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = guarded.Close() })
	defer stop()
	data, err = io.ReadAll(io.LimitReader(guarded, limit+1))
	if int64(len(data)) > limit {
		data, err = data[:limit], tn.ErrResponseTooLarge
	}
	if ctx.Err() != nil {
		return data, ctx.Err()
	}
	return data, err
}
