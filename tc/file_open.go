package tc

import (
	"context"
	"errors"
	"io"
	"os"
)

// openRegularFile 对已打开的描述符检查类型，避免路径预检查与打开之间的替换窗口。
// Unix 使用非阻塞打开，FIFO 不会在获得描述符之前阻塞；普通文件的读取语义不变。
func openRegularFile(ctx context.Context, open func(int) (*os.File, error)) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	file, err := open(regularFileOpenFlags)
	if err != nil {
		return nil, 0, err
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	if !stat.Mode().IsRegular() {
		_ = file.Close()
		return nil, 0, errors.New("file body requires a regular file")
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	return file, stat.Size(), nil
}
