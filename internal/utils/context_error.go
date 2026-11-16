package utils

import "errors"

// MergeContextError 在期限复核时保留当前操作的失败原因，并附加观察到的 Context 错误。
// 成功但迟到时返回 Context 错误；没有取消或超时时直接复用原结果，不创建新错误。
// 已包含同一 Context 原因时不重复包装，便于多层复核后仍用 errors.Is/As 查询原始标记。
// ctxErr 应在调用方主动 cancel 之前读取；这里不判断业务重试策略，也不累计历次尝试。
func MergeContextError(err, ctxErr error) error {
	if ctxErr == nil {
		return err
	}
	if err == nil {
		return ctxErr
	}
	if errors.Is(err, ctxErr) {
		return err
	}
	return errors.Join(err, ctxErr)
}
