package requests

import (
	"github.com/dhlanshan/requests/dto"
	"github.com/dhlanshan/requests/internal/core"
	"net/http"
)

func NewClient(cmd dto.ClientParam) *http.Client {
	return core.NewClient(cmd)
}
