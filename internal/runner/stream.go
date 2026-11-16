package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/logging"
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
	started := time.Now()
	if ctx == nil || utils.IsNil(dst) || p.EnableValid {
		return 0, nil, rejectStream(ctx, p, started)
	}
	target := &streamTarget{writer: dst}
	_, header, err := execute(context.WithValue(ctx, streamKey{}, target), client, p, started)
	return target.written, header, err
}

// rejectStream 同样先完成 Body 清理，再派发参数错误或异常结束。
// 独立主体使 Close 中的 panic / Goexit 也不会跳过最终日志，或被记成普通参数失败。
func rejectStream(ctx context.Context, p tn.ApiParam, started time.Time) (err error) {
	var call *logging.Call
	returnedNormally := false
	defer func() {
		phase, endErr := logging.PhaseParameters, err
		if !returnedNormally {
			phase, endErr = logging.PhaseAborted, tn.ErrExecutionAborted
		}
		call.OnEnd(logContext(ctx), logging.CallResult{Duration: time.Since(started), Phase: phase, Err: endErr})
	}()
	err = func() error {
		if closer, ok := p.Body.(io.Closer); ok && !utils.IsNil(closer) {
			defer closer.Close()
		}
		call = startCallLogging(ctx, p, started)
		return errors.New("ApiTo requires a context, writer and disabled body validation")
	}()
	returnedNormally = true
	return
}
