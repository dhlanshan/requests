package core

import (
	"github.com/dhlanshan/requests/dto"
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/mdw"
	"net/http"
)

// NewClient Create New Client
func NewClient(cmd dto.ClientParam) *http.Client {
	spaceName := utils.TernaryOperator(cmd.SpaceName == "", "master", cmd.SpaceName)

	client, ok := clientStore.Load(spaceName)
	if ok && !cmd.Reset {
		return client
	}

	clientNew := &http.Client{}
	clientStore.Store(spaceName, clientNew)

	// 加载中间件
	mdwChain := mdw.NewMdwChain(cmd.Transport)
	tra := mdwChain.Add(cmd.Middlewares...)
	clientNew.Transport = tra

	return clientNew
}
