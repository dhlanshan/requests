package requests

import (
	"encoding/json"
	"fmt"
	"github.com/dhlanshan/requests/dto"
	"os"
	"testing"
)

func TestRaw(t *testing.T) {

	p := dto.ApiParam{
		Url:     "http://127.0.0.1:5000/test",
		Method:  "GET",
		Timeout: 800,
		EchoRes: true,
		EchoReq: true,
		Caller:  "回复几个号",
		TraceId: "12652657321654",
	}
	_, _, err := Api(nil, p)
	fmt.Println(err)
}

func TestXWFU(t *testing.T) {
	body, contentType, err := ContentByXWFormUrlencoded(map[string]any{
		"user": "zzz",
		"age":  18,
	})

	p := dto.ApiParam{
		Url:     "http://127.0.0.1:5000/test",
		Method:  "POST",
		Header:  map[string]string{"Content-Type": contentType},
		Body:    body,
		Timeout: 800,
		EchoRes: true,
		EchoReq: true,
		Caller:  "回复几个号",
		TraceId: "12652657321654",
	}
	_, _, err = Api(nil, p)
	fmt.Println(err)
}

func TestFormData(t *testing.T) {
	body, contentType, err := ContentByFormData(map[string]any{
		"user": "zzz",
		"age":  18,
		"c":    []int{1, 2, 3, 4},
		"cda": []FormFile{
			{
				Filename: "kl.png",
				Content:  "./api.go",
			},
			{
				Filename: "k5.txt",
				Content:  "./client.go",
			},
		},
	})

	p := dto.ApiParam{
		Url:     "http://127.0.0.1:5000/test",
		Method:  "POST",
		Header:  map[string]string{"Content-Type": contentType},
		Body:    body,
		Timeout: 800,
		EchoRes: true,
		EchoReq: true,
		Caller:  "回复几个号",
		TraceId: "12652657321654",
	}
	_, _, err = Api(nil, p)
	fmt.Println(err)
}

func TestJson(t *testing.T) {
	bodyType, _ := json.Marshal(map[string]any{
		"user": "zzz",
		"age":  18,
	})
	body, contentType, err := ContentByJson(bodyType)

	p := dto.ApiParam{
		Url:     "http://127.0.0.1:5000/test",
		Method:  "POST",
		Header:  map[string]string{"Content-Type": contentType},
		Body:    body,
		Timeout: 800,
		EchoRes: true,
		EchoReq: true,
		Caller:  "回复几个号",
		TraceId: "12652657321654",
	}
	_, _, err = Api(nil, p)
	fmt.Println(err)
}

func TestXMsgpack(t *testing.T) {
	body, contentType, err := ContentByMsgpack(map[string]any{
		"user": "zzz",
		"age":  18,
		"55":   []string{"a", "b", "c"},
	})

	p := dto.ApiParam{
		Url:     "http://127.0.0.1:5000/test",
		Method:  "POST",
		Header:  map[string]string{"Content-Type": contentType},
		Body:    body,
		Timeout: 800,
		EchoRes: true,
		EchoReq: true,
		Caller:  "回复几个号",
		TraceId: "12652657321654",
	}
	_, _, err = Api(nil, p)
	fmt.Println(err)
}

func TestBinary(t *testing.T) {
	file, err := os.Open("./api.go")
	if err != nil {
		file.Close()
	}
	defer file.Close()

	body, ct, contentType, err := ContentByBinary(file)

	p := dto.ApiParam{
		Url:     "http://127.0.0.1:5000/test",
		Method:  "POST",
		Header:  map[string]string{"Content-Type": contentType, "Content-Length": fmt.Sprintf("%d", ct)},
		Body:    body,
		Timeout: 800,
		EchoRes: true,
		EchoReq: true,
		Caller:  "回复几个号",
		TraceId: "12652657321654",
	}
	_, _, err = Api(nil, p)
	fmt.Println(err)
}
