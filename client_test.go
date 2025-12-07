package requests

import (
	"encoding/json"
	"fmt"
	"github.com/dhlanshan/requests/dto"
	"github.com/dhlanshan/requests/interfaces"
	"github.com/dhlanshan/requests/mdw"
	"net/http"
	"testing"
)

type Abc struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AwardNoticeTime string `json:"award_notice_time"`
		AwardRule       []any  `json:"award_rule"`
	} `json:"data"`
}

func AAValidator(respBody []byte, respHeader http.Header) bool {
	busStatus := true
	var rp Abc
	err := json.Unmarshal(respBody, &rp)
	if err != nil {
		return false
	}
	if rp.Code != 0 || rp.Msg != "ok" {
		busStatus = false
	}
	fmt.Println(respHeader.Get("date"))

	return busStatus
}

func BBValidator(respBody []byte, header http.Header) bool {
	busStatus := true

	var rp Abc
	err := json.Unmarshal(respBody, &rp)
	if err != nil {
		return false
	}
	if len(rp.Data.AwardRule) == 0 {
		busStatus = false
	}

	return busStatus
}

func TestClient(t *testing.T) {
	client := NewClient(dto.ClientParam{
		Middlewares: []interfaces.Middleware{&mdw.LoggingMiddleware{}, &mdw.RetryMiddleware{}},
	})

	p := dto.ApiParam{
		Url:         "https://wb-race-test.51sapience.com/bh/p/race_desc",
		Method:      "GET",
		Timeout:     800,
		EnableValid: true,
		Validator:   AAValidator,
	}
	respData, respHead, err := Api(client, p)
	fmt.Println(respData)
	fmt.Println(respHead)
	fmt.Println(err)
	p.Validator = BBValidator
	respData, respHead, err = Api(client, p)
	fmt.Println(respData)
	fmt.Println(respHead)
	fmt.Println(err)
}
