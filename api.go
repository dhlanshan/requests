package requests

import (
	"context"
	"github.com/dhlanshan/requests/dto"
	"github.com/dhlanshan/requests/internal/core"
	"net/http"
	"time"
)

func Api(client *http.Client, p dto.ApiParam) (body []byte, header http.Header, err error) {
	if p.Check() != nil {
		return nil, nil, err
	}
	if p.Timeout <= 0 {
		p.Timeout = 30
	}

	ctx, cancel := context.WithTimeout(context.Background(), p.Timeout*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, dto.CtxJKExtend{}, &p)

	req, err := core.NewRequest(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	return core.SendRequest(client, req)
}
