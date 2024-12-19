package requests

import (
	"encoding/json"
	"fmt"
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
	p := ApiParam{
		Url:         "https://wb-race-test.51sapience.com/bh/p/race_desc",
		Method:      "GET",
		EnableValid: true,
		Validator:   AAValidator,
	}
	respData, respHead, err := Api(nil, p)
	fmt.Println(respData)
	fmt.Println(respHead)
	fmt.Println(err)
	p.Validator = BBValidator
	respData, respHead, err = Api(nil, p)
	fmt.Println(respData)
	fmt.Println(respHead)
	fmt.Println(err)
}
