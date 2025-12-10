package requests

import (
	"github.com/dhlanshan/requests/internal/core"
	"github.com/dhlanshan/requests/tn"
	"net/http"
)

func NewClient(cmd tn.ClientParam) *http.Client {
	return core.NewClient(cmd)
}
