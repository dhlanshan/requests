package mdw

import (
	"errors"
	"fmt"
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/internal/validator"
	"github.com/dhlanshan/requests/pf"
	"net/http"
	"time"
)

type RetryMiddleware struct {
	Transport http.RoundTripper
}

func (mw *RetryMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	meta, _ := pf.GetRequestMeta(req.Context())

	i := 0
	for {
		resp, err := mw.Transport.RoundTrip(req) // 调用下一个中间件
		if err != nil {
			fmt.Println(err.Error())
		}

		if err == nil {
			if !meta.EnableValid {
				return resp, nil
			}
			_validator := utils.TernaryOperator(meta.Validator == nil, validator.DefaultValidator, meta.Validator)
			data, err := pf.ReadResponseBody(resp)
			if err != nil {
				fmt.Println(err.Error())
			}
			if _validator(data, resp.Header) {
				return resp, nil
			}
		}
		if meta.Retry == i {
			newErr := utils.TernaryOperator(err != nil, err, errors.New("response data verification failed"))
			return resp, newErr

		}
		i += 1
		if meta.RetryInterval > 0 {
			time.Sleep(meta.RetryInterval)
		}
	}
}

func (mw *RetryMiddleware) SetTransport(rt http.RoundTripper) {
	mw.Transport = rt
}
