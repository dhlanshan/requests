package mdw

import (
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/internal/validator"
	"github.com/dhlanshan/requests/pf"
	"net/http"
	"time"
)

type RetryData struct {
	TryCnt int `json:"try_cnt"`
}

type RetryMiddleware struct {
	Transport http.RoundTripper
}

func (mw *RetryMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	meta, _ := pf.GetRequestMeta(req.Context())
	busMeta, _ := pf.GetBusMeta(req.Context())

	i := 0
	for {
		resp, err := mw.Transport.RoundTrip(req)
		var validErr error
		if err == nil && resp != nil {
			if !meta.EnableValid {
				busMeta.MdwDataMap.Store(mw.Name(), &RetryData{TryCnt: i})
				return resp, nil
			}

			var _f bool
			_validator := utils.TernaryOperator(meta.Validator == nil, validator.DefaultValidator, meta.Validator)
			respBody, _ := pf.ReadResponseBody(resp)
			if _f, validErr = validator.SafeValidate(_validator, respBody, resp.Header); _f {
				busMeta.MdwDataMap.Store(mw.Name(), &RetryData{TryCnt: i})
				return resp, nil
			}
		}

		if meta.Retry == i {
			newErr := utils.TernaryOperator(err != nil, err, validErr)
			busMeta.MdwDataMap.Store(mw.Name(), &RetryData{TryCnt: i})
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

func (mw *RetryMiddleware) Name() string {
	return "Retry Middleware"
}
