//go:build !go1.24

package tc

import (
	"context"
	"errors"
	"io"

	"github.com/dhlanshan/requests/tn"
)

// FileBodyFactoryUnder 在旧工具链上明确拒绝，避免退化为存在 TOCTOU 窗口的路径前缀检查。
// 保留 Go 1.23 源码兼容；生产需使用支持 os.Root 的安全补丁工具链。
func FileBodyFactoryUnder(root, relative string) tn.BodyFactory {
	return func(context.Context) (io.ReadCloser, int64, error) {
		return nil, 0, errors.New("FileBodyFactoryUnder requires Go 1.24 or later")
	}
}
