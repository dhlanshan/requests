package pf

import (
	"github.com/dhlanshan/requests/internal/lz"
	"net/http"
)

// ReadRequestBody 读取并恢复 Body，默认上限 8MiB；调用方必须处理返回错误。
func ReadRequestBody(req *http.Request) ([]byte, error) { return lz.ReadAndRestoreRequestBody(req) }

// ReadResponseBody 读取并恢复 Body，默认上限 16MiB。
func ReadResponseBody(resp *http.Response) ([]byte, error) {
	return lz.ReadAndRestoreResponseBody(resp)
}

func ReadRequestBodyLimit(req *http.Request, limit int64) ([]byte, error) {
	return lz.ReadRequestLimit(req, limit)
}
func ReadResponseBodyLimit(resp *http.Response, limit int64) ([]byte, error) {
	return lz.ReadResponseLimit(resp, limit)
}
