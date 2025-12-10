package core

import (
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/mdw"
	"github.com/dhlanshan/requests/tn"
	"net/http"
	"sync"
	"time"
)

var (
	defaultClient *http.Client
	clientOnce    sync.Once
)

// NewClient Create New Client
func NewClient(cmd tn.ClientParam) *http.Client {
	spaceName := utils.TernaryOperator(cmd.SpaceName == "", "master", cmd.SpaceName)

	client, ok := clientStore.Load(spaceName)
	if ok && !cmd.Reset {
		return client
	}

	clientNew := &http.Client{}
	// 加载中间件
	mdwChain := mdw.NewMdwChain(cmd.Transport)
	tra := mdwChain.Add(cmd.Middlewares...)
	clientNew.Transport = tra

	if cmd.Timeout > 0 {
		clientNew.Timeout = cmd.Timeout * time.Second
	}

	clientStore.Store(spaceName, clientNew)
	return clientNew
}

func NewDefaultClient() *http.Client {
	clientOnce.Do(func() {
		defaultClient = &http.Client{}
		transport := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          500,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true,
		}
		mdwChain := mdw.NewMdwChain(transport)
		tra := mdwChain.Add(&mdw.LoggingMiddleware{}, &mdw.RetryMiddleware{})
		defaultClient.Transport = tra
	})
	return defaultClient
}
