package interfaces

import "net/http"

type Middleware interface {
	http.RoundTripper
	SetTransport(rt http.RoundTripper)
	Name() string
}
