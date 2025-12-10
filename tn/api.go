package tn

import (
	"errors"
	"github.com/dhlanshan/requests/internal/enum"
	"github.com/dhlanshan/requests/internal/utils"
	"io"
	"net/http"
	"time"
)

type CtxJKExtend struct{}
type Validator func(respBody []byte, respHeader http.Header) (bool, error)

type ApiParam struct {
	// Url is the target URL for the HTTP request.
	Url string `json:"url"`

	// Method defines the HTTP request method (e.g., "GET", "POST", etc.).
	Method string `json:"method"`

	// Header contains custom headers to be sent with the HTTP request.
	Header map[string]string `json:"header"`

	// Params holds the query parameters to be appended to the URL.
	Params map[string]string `json:"params"`

	// Body is the request body that will be sent with the HTTP request.
	// It should be used for methods like POST or PUT.
	// ContentByXWFormUrlencoded ContentByFormData ContentByJson ContentByXml ...
	Body io.Reader `json:"body"`

	// 扩展信息
	// Timeout defines the maximum amount of time to wait for the request to complete.
	Timeout time.Duration `json:"timeout"`

	// Retry specifies the number of times the request should be retried in case of failure.
	Retry int `json:"retry"`

	// RetryInterval defines the interval between retries (in seconds).
	RetryInterval time.Duration `json:"retry_interval"`

	// EnableValid indicates whether to enable response validation.
	// If true, the response will be validated using the Validator function.
	EnableValid bool `json:"enable_valid"`

	// Validator is a custom function that will be used to validate the response.
	// If it's nil, a default validator will be used.
	Validator Validator `json:"validator"`

	// Caller represents the name or identifier of the caller (e.g., the module or user initiating the request).
	Caller string `json:"caller"`

	// EchoReq determines whether to log the request data for debugging or tracing.
	EchoReq bool `json:"echo_req"`

	// EchoRes determines whether to log the response data for debugging or tracing.
	EchoRes bool `json:"echo_res"`

	// TraceId is the unique identifier attached to each request, allowing downstream services to correlate logs and traces.
	// Need to enable log printing function, EchoReq and EchoRes
	TraceId string `json:"trace_id"`
}

func (a *ApiParam) Check() error {
	if a.Url == "" {
		return errors.New("url is empty")
	}
	if a.Method == "" || !utils.InSlice(enum.MethodList, enum.MethodEnum(a.Method)) {
		return errors.New("invalid method")
	}
	return nil
}
