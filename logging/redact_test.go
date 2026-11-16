package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// 同一个只读配置同时用于多个请求/响应，验证规范化、递归和快照隔离。
func TestDefaultRedactorStructuredRulesConcurrent(t *testing.T) {
	d := DefaultRedactor{SensitiveFields: []string{"m_o.b-ile", "id_card", "Ädresse", ""}}
	for _, test := range []struct {
		name, contentType, input, want string
	}{
		{
			name: "json", contentType: "application/problem+json",
			input: `{"Mo.BI_LE":"phone","nested":[{"ID.CA_RD":"identity","Pass-Word":"password","To.Ken":"token","keep":"yes"}],"ÄD_RE.SSE":"street","...":"empty-key"}`,
			want:  `{"Mo.BI_LE":"[REDACTED]","nested":[{"ID.CA_RD":"[REDACTED]","Pass-Word":"[REDACTED]","To.Ken":"[REDACTED]","keep":"yes"}],"ÄD_RE.SSE":"[REDACTED]","...":"[REDACTED]"}`,
		},
		{
			name: "form", contentType: "application/x-www-form-urlencoded; charset=utf-8",
			input: "Mo.BI_LE=phone&ID.CA_RD=one&ID.CA_RD=two&Pass-Word=password&To.Ken=token&keep=yes&%C3%84D_RE.SSE=street&...=empty-key",
			want:  "Mo.BI_LE=%5BREDACTED%5D&ID.CA_RD=%5BREDACTED%5D&Pass-Word=%5BREDACTED%5D&To.Ken=%5BREDACTED%5D&keep=yes&%C3%84D_RE.SSE=%5BREDACTED%5D&...=%5BREDACTED%5D",
		},
	} {
		for worker := range 8 {
			t.Run(fmt.Sprintf("%s/%d", test.name, worker), func(t *testing.T) {
				t.Parallel()
				data := []byte(test.input)
				body := BodySnapshot{Data: data, Complete: true}
				request := d.RedactRequest(RequestSnapshot{ContentType: test.contentType, Body: body})
				response := d.RedactResponse(ResponseSnapshot{ContentType: test.contentType, Body: body})
				for _, got := range []BodySnapshot{request.Body, response.Body} {
					if got.OmittedReason != "" {
						t.Fatalf("body omitted: %s", got.OmittedReason)
					}
					if test.name == "json" {
						var actual, expected any
						if err := json.Unmarshal(got.Data, &actual); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal([]byte(test.want), &expected); err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(actual, expected) {
							t.Fatalf("redacted JSON=%s want=%s", got.Data, test.want)
						}
					} else {
						actual, err := url.ParseQuery(string(got.Data))
						if err != nil {
							t.Fatal(err)
						}
						expected, err := url.ParseQuery(test.want)
						if err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(actual, expected) {
							t.Fatalf("redacted form=%v want=%v", actual, expected)
						}
					}
				}
				if !bytes.Equal(data, []byte(test.input)) {
					t.Fatal("redactor modified the input body")
				}
			})
		}
	}
}

func BenchmarkDefaultRedactorJSON(b *testing.B) {
	var body strings.Builder
	body.WriteByte('{')
	for field := range 300 {
		if field != 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, "\"k%03d\":1", field)
	}
	body.WriteByte('}')
	request := RequestSnapshot{URL: "https://example.test", ContentType: "application/json",
		Body: BodySnapshot{Data: []byte(body.String()), Complete: true}}
	for _, test := range []struct {
		name   string
		fields []string
	}{
		{name: "default"},
		{name: "extra_2", fields: []string{"mobile", "id_card"}},
	} {
		b.Run(test.name, func(b *testing.B) {
			d := DefaultRedactor{SensitiveFields: test.fields}
			b.ReportAllocs()
			b.SetBytes(int64(len(request.Body.Data)))
			var result RequestSnapshot
			for b.Loop() {
				result = d.RedactRequest(request)
			}
			if !json.Valid(result.Body.Data) || result.Body.OmittedReason != "" {
				b.Fatal("benchmark did not produce a redacted JSON body")
			}
		})
	}
}
