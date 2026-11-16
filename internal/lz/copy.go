package lz

import (
	"context"
	"errors"
	"io"
	"math"

	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/tn"
)

// CopyBounded 复制至多 limit 字节，再探测一个字节区分精确长度与超限。
// 不向目标写入探测字节，不无限排空超限响应；缓冲区固定为 32KiB。
func CopyBounded(ctx context.Context, dst io.Writer, body io.ReadCloser, limit int64) (written int64, err error) {
	body = Guard(body)
	defer func() {
		if closeErr := body.Close(); err == nil {
			err = closeErr
		}
	}()
	if limit <= 0 || limit == math.MaxInt64 {
		return 0, errors.New("invalid body limit")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	buf := make([]byte, 32<<10)
	reader := io.LimitReader(body, limit)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			count, writeErr := dst.Write(buf[:n])
			if count < 0 || count > n {
				return written, errors.Join(tn.ErrResponseWrite, errors.New("invalid write count"))
			}
			written += int64(count)
			if writeErr != nil {
				return written, errors.Join(tn.ErrResponseWrite, writeErr)
			}
			if count != n {
				return written, errors.Join(tn.ErrResponseWrite, io.ErrShortWrite)
			}
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			// EOF 本身表示正常结束，不作为失败原因附加；其他读取错误必须保留。
			if readErr == io.EOF {
				readErr = nil
			}
			return written, utils.MergeContextError(readErr, ctxErr)
		}
		if readErr != nil {
			if readErr != io.EOF {
				return written, readErr
			}
			break
		}
	}
	if written == limit {
		var extra [1]byte
		n, readErr := io.ReadFull(body, extra[:])
		if n > 0 {
			// 已观察到超限，不能让同时发生的超时擦除容量错误。
			return written, utils.MergeContextError(tn.ErrResponseTooLarge, ctx.Err())
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			if readErr == io.EOF {
				readErr = nil
			}
			return written, utils.MergeContextError(readErr, ctxErr)
		}
		if readErr != io.EOF {
			return written, readErr
		}
	}
	return written, nil
}
