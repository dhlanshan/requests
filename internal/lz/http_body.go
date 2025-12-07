package lz

import (
	"bytes"
	"io"
	"net/http"
)

func ReadAndRestoreRequestBody(req *http.Request) ([]byte, error) {
	if req == nil || req.Body == nil {
		return make([]byte, 0), nil
	}

	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(data))

	return data, nil
}

func ReadAndRestoreResponseBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return make([]byte, 0), nil
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	return bodyBytes, nil
}
