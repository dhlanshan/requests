package validator

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dhlanshan/requests/tn"
)

// Validate 同步校验，不启动无法停止的后台 goroutine。
// 回调须配合 Context 取消；返回时再次检查期限，避免超时后报告成功。
func Validate(ctx context.Context, fn tn.Validator, body []byte, header http.Header) (ok bool, err error) {
	if err = ctx.Err(); err != nil {
		return false, err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			ok, err = false, fmt.Errorf("%w: validator panic (%T)", tn.ErrValidation, recovered)
		}
		if ctx.Err() != nil {
			ok, err = false, ctx.Err()
		}
	}()
	if fn == nil {
		return true, nil
	}
	ok, err = fn(ctx, body, header)
	if err != nil {
		return false, fmt.Errorf("%w: %w", tn.ErrValidation, err)
	}
	if !ok {
		return false, tn.ErrValidation
	}
	return true, nil
}
