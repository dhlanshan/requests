package requests

import (
	"errors"
	"github.com/dhlanshan/requests/internal/enum"
	"github.com/dhlanshan/requests/internal/tools"
	"net/http"
	"time"
)

type ApiParam struct {
	// Url is the target URL for the HTTP request.
	Url string `json:"url"`

	// Method defines the HTTP request method (e.g., "GET", "POST", etc.).
	Method string `json:"method"`

	// Header contains custom headers to be sent with the HTTP request.
	Header map[string]string `json:"header"`

	// Params holds the query parameters to be appended to the URL.
	Params map[string]string `json:"params"`

	// Data is the request body that will be sent with the HTTP request.
	// It should be used for methods like POST or PUT.
	Data []byte `json:"data"`

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

	// SpaceName is a unique identifier for a space or context that the client belongs to.
	// It's useful for reusing the same client for requests within the same space.
	SpaceName string `json:"space_name"`

	// EchoReq determines whether to log the request data for debugging or tracing.
	EchoReq bool `json:"echo_req"`

	// EchoRes determines whether to log the response data for debugging or tracing.
	EchoRes bool `json:"echo_res"`

	// Reset indicates whether to reset the HTTP client before sending the request.
	// If true, the client will be reinitialized.
	Reset bool `json:"reset"`

	// Timeout defines the maximum amount of time to wait for the request to complete.
	Timeout time.Duration `json:"timeout"`

	// Transport is an optional custom RoundTripper that can be used to customize the HTTP request behavior.
	Transport http.RoundTripper `json:"transport"`

	// CheckRedirect is a function that will be used to handle HTTP redirects.
	// It can be used to customize how redirects are followed.
	CheckRedirect CheckRedirectFunc `json:"check_redirect"`

	// Jar is an optional cookie jar that can store cookies for the request.
	Jar http.CookieJar `json:"jar"`
}

func (a *ApiParam) Check() error {
	if a.Url == "" {
		return errors.New("url is empty")
	}
	if a.Method == "" || !tools.InSlice(enum.MethodList, enum.MethodEnum(a.Method)) {
		return errors.New("invalid method")
	}
	return nil
}
