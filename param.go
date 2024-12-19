package requests

import (
	"errors"
	"github.com/dhlanshan/requests/internal/enum"
	"github.com/dhlanshan/requests/internal/tools"
	"net/http"
	"time"
)

type ApiParam struct {
	Url           string            `json:"url"`            // url
	Method        string            `json:"method"`         // 请求类型
	Header        map[string]string `json:"header"`         // 请求头
	Params        map[string]string `json:"params"`         // url参数
	Data          []byte            `json:"data"`           // 请求体
	Retry         int               `json:"retry"`          // 重试次数
	RetryInterval time.Duration     `json:"retry_interval"` // 重试间隔, 单位: 秒
	EnableValid   bool              `json:"enable_valid"`   // 启用校验
	Validator     Validator         `json:"validator"`      // 校验器
	Caller        string            `json:"caller"`         // 调用者
	SpaceName     string            `json:"space_name"`     // 空间名称
	EchoReq       bool              `json:"echo_req"`       // 打印请求日志
	EchoRes       bool              `json:"echo_res"`       // 打印响应日志
	Reset         bool              `json:"reset"`          // 重置http客户端
	Timeout       time.Duration     `json:"timeout"`        // 超时时间
	Transport     http.RoundTripper `json:"transport"`
	CheckRedirect CheckRedirectFunc
	Jar           http.CookieJar
}

func (a *ApiParam) Check() error {
	if a.Url == "" {
		return errors.New("url is empty")
	}
	if a.Method == "" || !tools.InSlice(enum.MethodList, enum.MethodEnum(a.Method)) {
		return errors.New("invalid method")
	}
	return nil
}
