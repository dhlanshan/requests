package pf

import (
	"github.com/dhlanshan/requests/internal/lz"
	"net/http"
)

func ReadRequestBody(req *http.Request) ([]byte, error) {
	return lz.ReadAndRestoreRequestBody(req)
}

func ReadResponseBody(resp *http.Response) ([]byte, error) {
	return lz.ReadAndRestoreResponseBody(resp)
}
