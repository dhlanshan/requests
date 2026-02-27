package mdw

import (
	"bytes"
	"context"
	"fmt"
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/internal/validator"
	"github.com/dhlanshan/requests/pf"
	"io"
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

	// 处理 Body rewind
	var (
		bodyBytes []byte
		err       error
	)

	if req.Body != nil && req.GetBody == nil {
		bodyBytes, err = pf.ReadRequestBody(req)
	}

	i := 0
	for {
		// 检查父 ctx 是否已经结束
		if ctxErr := req.Context().Err(); ctxErr != nil {
			busMeta.MdwDataMap.Store(mw.Name(), &RetryData{TryCnt: i})
			return nil, fmt.Errorf("request timed out0")
		}

		// 计算本次 attempt 的 timeout, 单次超时不能超过父 ctx 剩余时间
		timeout := meta.SingleTimeout
		if deadline, ok := req.Context().Deadline(); ok {
			remain := time.Until(deadline)
			if remain <= 0 {
				busMeta.MdwDataMap.Store(mw.Name(), &RetryData{TryCnt: i})
				return nil, fmt.Errorf("request timed out1")
			}
			if timeout <= 0 || timeout > remain {
				timeout = remain
			}
		}

		// 创建本次 attempt ctx（独立子 ctx）,不会影响父 ctx，不会 double cancel
		var (
			attemptCtx context.Context
			cancel     context.CancelFunc
		)
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(req.Context(), timeout)
		} else {
			// 如果没有设置单次 timeout，直接继承父 ctx
			attemptCtx, cancel = context.WithCancel(req.Context())
		}

		// 处理请求的body
		newReq := req.Clone(attemptCtx)
		if req.GetBody != nil {
			newReq.Body, err = req.GetBody()
			if err != nil {
				cancel()
				return nil, err
			}
		} else if bodyBytes != nil {
			newReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		//调用下一个中间件
		resp, err := mw.Transport.RoundTrip(newReq)

		// 处理响应
		respBody, _ := pf.ReadResponseBody(resp)
		cancel()
		if len(respBody) > 0 {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
		}

		var validErr error
		if err == nil && resp != nil {
			if !meta.EnableValid {
				busMeta.MdwDataMap.Store(mw.Name(), &RetryData{TryCnt: i})
				return resp, nil
			}

			var _f bool
			_validator := utils.TernaryOperator(meta.Validator == nil, validator.DefaultValidator, meta.Validator)
			//respBody, _ := pf.ReadResponseBody(resp)
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

		// retry 前关闭 resp.Body 防止泄漏
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}

		// 再次确认父 ctx 是否结束（避免无意义 retry）
		if ctxErr := req.Context().Err(); ctxErr != nil {
			busMeta.MdwDataMap.Store(mw.Name(), &RetryData{TryCnt: i})
			return nil, fmt.Errorf("request timed out2")
		}

		i += 1
		if meta.RetryInterval > 0 {
			sleep := meta.RetryInterval
			// 如果父 ctx 有 deadline，不能超过剩余时间
			if deadline, ok := req.Context().Deadline(); ok {
				remain := time.Until(deadline)
				if remain <= 0 {
					busMeta.MdwDataMap.Store(mw.Name(), &RetryData{TryCnt: i})
					return nil, req.Context().Err()
				}
				if sleep > remain {
					sleep = remain
				}
			}
			timer := time.NewTimer(sleep)

			select {
			case <-timer.C:
				timer.Stop()
			case <-req.Context().Done():
				timer.Stop()
				busMeta.MdwDataMap.Store(mw.Name(), &RetryData{TryCnt: i})
				return nil, fmt.Errorf("request timed out3")
			}
		}
	}
}

func (mw *RetryMiddleware) SetTransport(rt http.RoundTripper) {
	mw.Transport = rt
}

func (mw *RetryMiddleware) Name() string {
	return "Retry Middleware"
}
