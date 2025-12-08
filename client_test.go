package requests

import (
	"encoding/json"
	"fmt"
	"github.com/dhlanshan/requests/dto"
	"net/http"
	"testing"
	"time"
)

type Abc struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AwardNoticeTime string `json:"award_notice_time"`
		AwardRule       []any  `json:"award_rule"`
	} `json:"data"`
}

func AAValidator(respBody []byte, respHeader http.Header) (bool, error) {
	var err error
	busStatus := true
	var rp Abc
	err = json.Unmarshal(respBody, &rp)
	if err != nil {
		return false, fmt.Errorf("json unmarshal err: %v", err)
	}
	if rp.Code != 0 || rp.Msg != "ok" {
		busStatus = false
	}
	fmt.Println(respHeader.Get("date"))

	return busStatus, err
}

func BBValidator(respBody []byte, header http.Header) (bool, error) {
	busStatus := true
	var err error
	var rp Abc
	err = json.Unmarshal(respBody, &rp)
	if err != nil {
		return false, fmt.Errorf("json unmarshal err: %v", err)
	}
	if len(rp.Data.AwardRule) == 0 {
		busStatus = false
		err = fmt.Errorf("award_notice_time: %s", rp.Data.AwardNoticeTime)
	}

	return busStatus, err
}

func TestClient(t *testing.T) {
	//client := NewClient(dto.ClientParam{
	//	Middlewares: []interfaces.Middleware{&mdw.LoggingMiddleware{}, &mdw.RetryMiddleware{}},
	//})

	p := dto.ApiParam{
		Url:           "https://wb-race-test.51sapience.com/bh/p/race_desc",
		Method:        "GET",
		Timeout:       800,
		EchoRes:       true,
		EchoReq:       true,
		Caller:        "回复几个号",
		EnableValid:   true,
		Validator:     AAValidator,
		Retry:         5,
		RetryInterval: 3 * time.Second,
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
