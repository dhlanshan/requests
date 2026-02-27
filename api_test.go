package requests

import (
	"encoding/json"
	"fmt"
	"github.com/dhlanshan/requests/tc"
	"github.com/dhlanshan/requests/tn"
	"os"
	"sync"
	"testing"
	"time"
)

func TestRaw(t *testing.T) {

	p := tn.ApiParam{
		Url:           "http://127.0.0.1:5000/test",
		Method:        "GET",
		SingleTimeout: 5 * time.Second,
		Retry:         2,
		RetryInterval: 2 * time.Second,
		EchoRes:       true,
		EchoReq:       true,
		Caller:        "回复几个号",
		TraceId:       "12652657321654",
	}
	_, _, err := Api(nil, p)
	fmt.Println(err)
}

func TestXWFU(t *testing.T) {
	body, contentType, err := tc.ContentByXWFormUrlencoded(map[string]any{
		"user": "zzz",
		"age":  18,
	})

	p := tn.ApiParam{
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
	body, contentType, err := tc.ContentByFormData(map[string]any{
		"user": "zzz",
		"age":  18,
		"c":    []int{1, 2, 3, 4},
		"cda": []tc.FormFile{
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

	p := tn.ApiParam{
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
	body, contentType, err := tc.ContentByJson(bodyType)

	p := tn.ApiParam{
		Url:           "http://127.0.0.1:5000/test",
		Method:        "POST",
		Header:        map[string]string{"Content-Type": contentType},
		Body:          body,
		SingleTimeout: 5 * time.Second,
		Retry:         2,
		RetryInterval: 2 * time.Second,
		EchoRes:       true,
		EchoReq:       true,
		Caller:        "回复几个号",
		TraceId:       "12652657321654",
	}
	_, _, err = Api(nil, p)
	fmt.Println(err)
}

func TestXMsgpack(t *testing.T) {
	body, contentType, err := tc.ContentByMsgpack(map[string]any{
		"user": "zzz",
		"age":  18,
		"55":   []string{"a", "b", "c"},
	})

	p := tn.ApiParam{
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

	body, ct, contentType, err := tc.ContentByBinary(file)

	p := tn.ApiParam{
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

// 服务端延迟超过 SingleTimeout（触发 retry）
func TestRetryOnSingleTimeout(t *testing.T) {
	p := tn.ApiParam{
		Url:           "http://127.0.0.1:5000/test",
		Method:        "GET",
		SingleTimeout: 2 * time.Second,
		Retry:         2,
		RetryInterval: 1 * time.Second,
	}

	start := time.Now()
	a, _, err := Api(nil, p)
	fmt.Println(string(a))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error")
	}

	// 2s +1s +2s +1s +2s ≈ 8s
	if elapsed < 7*time.Second || elapsed > 9*time.Second {
		t.Fatalf("unexpected elapsed time: %v", elapsed)
	}
}

// 父 ctx 超时优先生效
func TestParentTimeout(t *testing.T) {

	p := tn.ApiParam{
		Url:           "http://127.0.0.1:5000/test",
		Method:        "GET",
		SingleTimeout: 5 * time.Second,
		Timeout:       3 * time.Second,
		Retry:         5,
		RetryInterval: 1 * time.Second,
	}

	start := time.Now()
	a, _, err := Api(nil, p)
	fmt.Println(string(a))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected parent timeout")
	}

	if err != nil {
		fmt.Println(err.Error())
	}

	if elapsed > 4*time.Second {
		t.Fatalf("should stop by parent timeout, got %v", elapsed)
	}
}

func TestRetryOnValidatorFail(t *testing.T) {
	p := tn.ApiParam{
		Url:         "http://127.0.0.1:5000/test",
		Method:      "GET",
		Retry:       3,
		EnableValid: true,
		Validator:   AAValidator,
	}

	resp, _, err := Api(nil, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fmt.Println(string(resp))

	if string(resp) != "ok" {
		t.Fatalf("expected ok, got %s", resp)
	}
}

// POST Body 是否可重复发送
func TestPostBodyRewind(t *testing.T) {
	bodyType, _ := json.Marshal(map[string]any{
		"user": "zzz",
		"age":  18,
	})
	body, contentType, err := tc.ContentByJson(bodyType)

	p := tn.ApiParam{
		Url:           "http://127.0.0.1:5000/test",
		Method:        "POST",
		Header:        map[string]string{"Content-Type": contentType},
		Body:          body,
		SingleTimeout: 1 * time.Second,
		EchoRes:       true,
		EchoReq:       true,
		Retry:         2,
	}

	resp, _, err := Api(nil, p)
	fmt.Println(string(resp))
	if err != nil {
		t.Fatal(err)
	}

}

func TestHighConcurrency(t *testing.T) {
	time.Sleep(20 * time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			p := tn.ApiParam{
				Url:    "http://127.0.0.1:5000/test",
				Method: "GET",
				Retry:  2,
			}
			Api(nil, p)

		}()
	}
	wg.Wait()
}
