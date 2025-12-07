package core

import (
	"github.com/dhlanshan/requests/dto"
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/mdw"
	"net/http"
	"time"
)

// NewClient Create New Client
func NewClient(cmd dto.ClientParam) *http.Client {
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
