package core

import (
	"context"
	"net/http"

	"github.com/dhlanshan/requests/internal/lz"
	"github.com/dhlanshan/requests/internal/runner"
	"github.com/dhlanshan/requests/tn"
)

func NewRequest(ctx context.Context, p tn.ApiParam) (*http.Request, error) {
	return runner.NewRequest(ctx, p)
}

// SendRequest 保留内部底层入口；完整策略由 runner.Execute 执行。
func SendRequest(client *http.Client, req *http.Request) ([]byte, http.Header, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	data, err := lz.ReadBounded(req.Context(), resp.Body, lz.DefaultResponseLimit)
	if err != nil {
		return data, resp.Header, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return data, resp.Header, &tn.HTTPError{StatusCode: resp.StatusCode, Header: resp.Header, Body: data}
	}
	return data, resp.Header, nil
}
