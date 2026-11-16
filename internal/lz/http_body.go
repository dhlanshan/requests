package lz

import (
	"bytes"
	"errors"
	"io"
	"math"
	"net/http"

	"github.com/dhlanshan/requests/tn"
)

type restoredBody struct {
	io.Reader
	io.Closer
}
type errorReader struct{ err error }

func (r *errorReader) Read([]byte) (int, error) {
	if r.err == nil {
		return 0, io.EOF
	}
	err := r.err
	r.err = nil
	return 0, err
}

// restore 保留读取失败时的部分数据和原始错误；超限时把前缀接回剩余流，
// 不把一个过大的 Body 悄悄变成截断成功，也不丢失其关闭能力。
func restore(body io.ReadCloser, limit int64) ([]byte, io.ReadCloser, bool, error) {
	if limit <= 0 || limit == math.MaxInt64 {
		return nil, body, false, errors.New("invalid body limit")
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if int64(len(data)) > limit {
		return data[:limit], &restoredBody{Reader: io.MultiReader(bytes.NewReader(data), body), Closer: body}, false, tn.ErrResponseTooLarge
	}
	closeErr := body.Close()
	if err != nil {
		return data, io.NopCloser(io.MultiReader(bytes.NewReader(data), &errorReader{err: err})), false, err
	}
	return data, io.NopCloser(bytes.NewReader(data)), true, closeErr
}

func ReadAndRestoreRequestBody(req *http.Request) ([]byte, error) {
	return ReadRequestLimit(req, 8<<20)
}
func ReadAndRestoreResponseBody(resp *http.Response) ([]byte, error) {
	return ReadResponseLimit(resp, DefaultResponseLimit)
}

func ReadRequestLimit(req *http.Request, limit int64) ([]byte, error) {
	if req == nil || req.Body == nil {
		return []byte{}, nil
	}
	data, restored, complete, err := restore(req.Body, limit)
	req.Body = restored
	if complete {
		req.ContentLength = int64(len(data))
		if len(data) == 0 {
			// 已完整读取并关闭原流；保留 net/http 的已知空正文标记，避免凭空新增 chunked。
			// 显式设置的 TransferEncoding 由调用方持有，此处不改写。
			req.Body = http.NoBody
			req.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
		} else {
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
		}
	} else {
		req.GetBody = nil
	}
	return data, err
}
func ReadResponseLimit(resp *http.Response, limit int64) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return []byte{}, nil
	}
	data, restored, _, err := restore(resp.Body, limit)
	resp.Body = restored
	return data, err
}
