package requests

import (
	"context"
	"fmt"
	"github.com/dhlanshan/requests/internal/core"
	"github.com/dhlanshan/requests/internal/idgen"
	"github.com/dhlanshan/requests/tn"
	"net/http"
	"time"
)

func Api(client *http.Client, p tn.ApiParam) (body []byte, header http.Header, err error) {
	if p.Check() != nil {
		return nil, nil, err
	}
	if p.Timeout <= 0 {
		p.Timeout = 30
	}

	ctx, cancel := context.WithTimeout(context.Background(), p.Timeout*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, tn.CtxJKExtend{}, &p)
	ctx = context.WithValue(ctx, tn.CtxBSExtend{}, &tn.InternalBus{
		RequestId: fmt.Sprintf("R%s", idgen.GenKsuId()),
	})

	req, err := core.NewRequest(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	if client == nil {
		client = core.NewDefaultClient()
	}
	return core.SendRequest(client, req)
}
