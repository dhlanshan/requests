//go:build go1.24

package tc

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/dhlanshan/requests/tn"
)

// FileBodyFactoryUnder 将外部相对路径限制在可信 root 内，用 os.Root 防止目录穿越及符号链接逃逸。
// root 必须由业务授权；每次重放重新打开普通文件，不共享偏移。需要 Go 1.24 及以上。
func FileBodyFactoryUnder(root, relative string) tn.BodyFactory {
	return func(ctx context.Context) (io.ReadCloser, int64, error) {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if !filepath.IsLocal(relative) {
			return nil, 0, errors.New("file path must be local to the authorized root")
		}
		dir, err := os.OpenRoot(root)
		if err != nil {
			return nil, 0, err
		}
		defer dir.Close()
		return openRegularFile(ctx, func(flag int) (*os.File, error) {
			return dir.OpenFile(relative, flag, 0)
		})
	}
}
