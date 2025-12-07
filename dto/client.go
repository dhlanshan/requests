package dto

import (
	"github.com/dhlanshan/requests/interfaces"
	"net/http"
	"time"
)

type CheckRedirectFunc func(req *http.Request, via []*http.Request) error

type ClientParam struct {
	// Timeout defines the maximum amount of time to wait for the request to complete.
	Timeout time.Duration `json:"timeout"`

	// SpaceName is a unique identifier for a space or context that the client belongs to.
	// It's useful for reusing the same client for requests within the same space.
	SpaceName string `json:"space_name"`

	// Transport is an optional custom RoundTripper that can be used to customize the HTTP request behavior.
	Transport http.RoundTripper `json:"transport"`

	// Jar is an optional cookie jar that can store cookies for the request.
	Jar http.CookieJar `json:"jar"`

	// Middlewares
	Middlewares []interfaces.Middleware `json:"middlewares"`

	// CheckRedirect is a function that will be used to handle HTTP redirects.
	// It can be used to customize how redirects are followed.
	CheckRedirect CheckRedirectFunc `json:"check_redirect"`

	// Reset indicates whether to reset the HTTP client before sending the request.
	// If true, the client will be reinitialized.
	Reset bool `json:"reset"`
}
