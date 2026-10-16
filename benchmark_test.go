package requests

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dhlanshan/requests/tn"
)

// 仅测本地执行开销，不包含网络延迟；便于观察日志/重试未启用时的分配。
func BenchmarkApiSmallResponse(b *testing.B) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	p := tn.ApiParam{Url: "http://example.test", Method: "GET"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := Api(client, p); err != nil {
			b.Fatal(err)
		}
	}
}
