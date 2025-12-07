package mdw

import (
	"fmt"
	"net/http"
	"time"
)

type LoggingMiddleware struct {
	Transport http.RoundTripper
}

func (mw *LoggingMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	fmt.Println("Sending request to:", req.URL)
	resp, err := mw.Transport.RoundTrip(req) // 调用下一个中间件
	if err != nil {
		fmt.Println("Error sending request:", err.Error())
		return nil, err
	}
	fmt.Printf("Received response for %s in %v\n", req.URL, time.Since(start))
	return resp, nil
}

func (mw *LoggingMiddleware) SetTransport(rt http.RoundTripper) {
	mw.Transport = rt
}

func (mw *LoggingMiddleware) Name() string {
	return "Logging Middleware"
}
