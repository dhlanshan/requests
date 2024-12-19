package requests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dhlanshan/requests/internal/tools"
	"io"
	"net/http"
	"sync"
	"time"
)

var httpClient sync.Map

type CheckRedirectFunc func(req *http.Request, via []*http.Request) error
type Validator func(respBody []byte, respHeader http.Header) bool

// newClient Create New Client
func newClient(spaceName string, reset bool, transport http.RoundTripper, checkRedirect CheckRedirectFunc, jar http.CookieJar, timeout time.Duration) *http.Client {
	if spaceName == "" {
		spaceName = "master"
	}
	client, ok := httpClient.Load(spaceName)
	if ok && !reset {
		return client.(*http.Client)
	}
	clientNew := &http.Client{}
	httpClient.Store(spaceName, clientNew)
	if timeout != 0 {
		clientNew.Timeout = timeout
	}
	if checkRedirect != nil {
		clientNew.CheckRedirect = checkRedirect
	}
	if transport != nil {
		clientNew.Transport = transport
	}
	if jar != nil {
		clientNew.Jar = jar
	}

	return clientNew
}

// newRequest Create New Request Obj
func newRequest(p ApiParam) (*http.Request, error) {
	body := bytes.NewReader(p.Data)
	req, err := http.NewRequest(p.Method, p.Url, body)
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

// sendRequest Send Request
func sendRequest(client *http.Client, req *http.Request) (respBody []byte, respHead http.Header, err error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, errors.New(fmt.Sprintf("Status code exception! code:%d", resp.StatusCode))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	return data, resp.Header, nil
}

func defaultValidator(respBody []byte, respHeader http.Header) bool {
	checkFiled := []map[string]any{{"key": "code", "value": float64(200)}, {"key": "msg", "value": "success"}}
	busStatus := true

	var rp map[string]any
	err := json.Unmarshal(respBody, &rp)
	if err != nil {
		return false
	}
	for _, pc := range checkFiled {
		if rp[pc["key"].(string)] != pc["value"] {
			busStatus = false
			break
		}
	}

	return busStatus
}

func Api(ctx context.Context, p ApiParam) (body []byte, header http.Header, err error) {
	if err = p.Check(); err != nil {
		return nil, nil, err
	}
	client := newClient(p.SpaceName, p.Reset, p.Transport, p.CheckRedirect, p.Jar, p.Timeout)

	i := 0
	for {
		req, err := newRequest(p)
		if err != nil {
			return nil, nil, err
		}
		respBody, respHead, err := sendRequest(client, req)
		if err == nil {
			if !p.EnableValid {
				return respBody, respHead, nil
			}
			validator := tools.TernaryOperator(p.Validator == nil, defaultValidator, p.Validator)
			if validator(respBody, respHead) {
				return respBody, respHead, nil
			}
		}
		if p.Retry == i {
			newErr := tools.TernaryOperator(err != nil, err, errors.New("response data verification failed"))
			return respBody, respHead, newErr

		}
		i += 1
		if p.RetryInterval > 0 {
			time.Sleep(p.RetryInterval)
		}
	}
}
