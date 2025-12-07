package core

import (
	"context"
	"errors"
	"fmt"
	"github.com/dhlanshan/requests/dto"
	"io"
	"net/http"
)

// NewRequest Create New Request Obj
func NewRequest(ctx context.Context, p dto.ApiParam) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, p.Method, p.Url, p.Body)
	if err != nil {
		return nil, errors.New("new request error")
	}
	// Set Header
	for k, v := range p.Header {
		req.Header.Add(k, v)
	}
	// Set Query
	if len(p.Params) > 0 {
		query := req.URL.Query()
		for k, v := range p.Params {
			query.Add(k, v)
		}
		req.URL.RawQuery = query.Encode()
	}

	return req, nil
}

// SendRequest Send Request
func SendRequest(client *http.Client, req *http.Request) (respBody []byte, respHead http.Header, err error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("http status code: %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	return data, resp.Header, nil
}
