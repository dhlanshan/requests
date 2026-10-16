package runner

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/tn"
)

type streamKey struct{}
type streamTarget struct {
	writer  io.Writer
	written int64
}

func streamTargetFrom(ctx context.Context) *streamTarget {
	target, _ := ctx.Value(streamKey{}).(*streamTarget)
	return target
}

// ExecuteTo 按固定缓冲区写入成功正文，失败正文仍采用有界缓冲及正常错误处理。
// 写入开始后的失败不重试；Writer 由调用方持有，不能强制取消一个不配合取消的 Write。
func ExecuteTo(ctx context.Context, client *http.Client, p tn.ApiParam, dst io.Writer) (int64, http.Header, error) {
	if ctx == nil || utils.IsNil(dst) || p.EnableValid {
		if closer, ok := p.Body.(io.Closer); ok && !utils.IsNil(closer) {
			_ = closer.Close()
		}
		return 0, nil, errors.New("ApiTo requires a context, writer and disabled body validation")
	}
	target := &streamTarget{writer: dst}
	_, header, err := Execute(context.WithValue(ctx, streamKey{}, target), client, p)
	return target.written, header, err
}
