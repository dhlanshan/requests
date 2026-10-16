package runner

import (
	"context"
	"net/url"
	"slices"
	"testing"

	"github.com/dhlanshan/requests/tn"
)

func FuzzQueryPreservesValidParameters(f *testing.F) {
	f.Add("good=1&bad=%zz")
	f.Add("tag=a&tag=b")
	f.Add("name=%E4%BD%A0%E5%A5%BD")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 16384 {
			t.Skip()
		}
		address := "http://example.test/?" + raw
		parsed, parseURLErr := url.Parse(address)
		if parseURLErr != nil {
			return
		}
		original, parseErr := url.ParseQuery(parsed.RawQuery)
		req, err := NewRequest(context.Background(), tn.ApiParam{Url: address, Method: "GET", Params: map[string]string{"extra": "value"}})
		if parseErr != nil {
			if err == nil {
				t.Fatal("invalid query silently accepted")
			}
			return
		}
		if err != nil {
			return
		} // URL 本身可能含片段或非法控制字符，查询有效并不代表整个 URL 有效。
		for key, values := range original {
			if key == "extra" {
				continue
			}
			got := req.URL.Query()[key]
			if !slices.Equal(values, got) {
				t.Fatal("existing query value lost")
			}
		}
	})
}
