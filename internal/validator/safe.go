package validator

import (
	"context"
	"fmt"
	"github.com/dhlanshan/requests/tn"
	"net/http"
)

// SafeValidate 保留旧入口；非 nil 错误优先于 ok。
func SafeValidate(fn func([]byte, http.Header) (bool, error), body []byte, header http.Header) (bool, error) {
	return Validate(context.Background(), tn.ApiParam{Validator: fn}, body, header)
}

// Validate 同步校验，不启动无法停止的后台 goroutine。
// 回调须配合 Context 取消；返回时再次检查期限，避免超时后报告成功。
func Validate(ctx context.Context, p tn.ApiParam, body []byte, header http.Header) (ok bool, err error) {
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
	switch {
	case p.ContextValidator != nil:
		ok, err = p.ContextValidator(ctx, body, header)
	case p.Validator != nil:
		ok, err = p.Validator(body, header)
	default:
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: %w", tn.ErrValidation, err)
	}
	if !ok {
		return false, tn.ErrValidation
	}
	return true, nil
}
