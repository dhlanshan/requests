package mdw

import (
	"encoding/json"
	"fmt"
	"github.com/dhlanshan/requests/pf"
	"net/http"
	"time"
)

type LoggingMiddleware struct {
	Transport http.RoundTripper
}

func (mw *LoggingMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	meta, _ := pf.GetRequestMeta(req.Context())
	busMeta, _ := pf.GetBusMeta(req.Context())
	startTime := time.Now()
	if meta.EchoReq {
		ct := req.Header.Get("Content-Type")
		reqBody := []byte("文件内容不读取")
		if ct != "application/octet-stream" {
			reqBody, _ = pf.ReadRequestBody(req)
		}
		header, _ := json.Marshal(req.Header)
		params, _ := json.Marshal(meta.Params)
		nts := startTime.Format("2006-01-02 15:04:05.00000")

		msgFormat := "%s | [Request] | tid:%s | rid:%s | <%s> | %s | %s | header:%s | params:%s | req_body:%s | END"
		message := fmt.Sprintf(msgFormat, nts, meta.TraceId, busMeta.RequestId, meta.Caller, meta.Method, meta.Url, string(header), string(params), string(reqBody))
		println(message)
	}

	resp, err := mw.Transport.RoundTrip(req) // 调用下一个中间件

	retryMdw, _ := busMeta.MdwDataMap.Load("Retry Middleware")
	retryData := retryMdw.(*RetryData)

	endTime := time.Now()
	eTime := fmt.Sprintf("%.5fs", (float64(endTime.UnixMilli()-startTime.UnixMilli()))*0.001)
	nts := endTime.Format("2006-01-02 15:04:05.00000")
	if err != nil {
		if meta.EchoRes {
			msgFormat := "%s | [Request] | tid:%s | rid:%s | <%s> | 耗时:%s | 重试:%d | status:%s | resp_body:%s | error:%s | END"
			message := fmt.Sprintf(msgFormat, nts, meta.TraceId, busMeta.RequestId, meta.Caller, eTime, retryData.TryCnt, "fail", "", err.Error())
			println(message)
		}
		return nil, err
	}

	if meta.EchoRes {
		respBody, _ := pf.ReadResponseBody(resp)
		msgFormat := "%s | [Request] | tid:%s | rid:%s | <%s> | 耗时:%s | 重试:%d | status:%s | resp_body:%s | error:%s | END"
		message := fmt.Sprintf(msgFormat, nts, meta.TraceId, busMeta.RequestId, meta.Caller, eTime, retryData.TryCnt, "ok", string(respBody), "")
		println(message)
	}
	return resp, nil
}

func (mw *LoggingMiddleware) SetTransport(rt http.RoundTripper) {
	mw.Transport = rt
}

func (mw *LoggingMiddleware) Name() string {
	return "Logging Middleware"
}
