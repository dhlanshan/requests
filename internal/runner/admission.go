package runner

import (
	"math"
	"net/http"

	"github.com/dhlanshan/requests/logging"
	"github.com/dhlanshan/requests/tn"
)

// bufferBudget 只在显式启用共享正文容量限制时调用。
// 返回值是按配置上限估算的名额预算，不代表已分配内存，也不是进程峰值内存上限。
func bufferBudget(p tn.ApiParam, req *http.Request, retry, streaming bool, capture logging.CaptureOptions) (int64, error) {
	budget := p.MaxResponseBytes
	if streaming {
		errorLimit := p.MaxErrorBytes
		if errorLimit > p.MaxResponseBytes {
			errorLimit = p.MaxResponseBytes
		}
		if errorLimit > math.MaxInt64-(32<<10) {
			return 0, tn.ErrAdmissionTooLarge
		}
		budget = errorLimit + (32 << 10)
	}
	oneShot := p.Body != nil && req.GetBody == nil && p.BodyFactory == nil
	if source, ok := p.Body.(tn.BodySource); ok {
		oneShot = !source.Replayable()
	}
	if length, explicit := declaredContentLength(p.Header); explicit && length == 0 {
		oneShot = false // 验证空正文后直接重放 NoBody，不分配重试缓存。
	}
	if retry && p.Retry > 0 && oneShot {
		if p.MaxReplayBytes > math.MaxInt64-budget {
			return 0, tn.ErrAdmissionTooLarge
		}
		budget += p.MaxReplayBytes
	}
	// 每次尝试至多同时保留一份上传/下载采集缓存；不累计历次正文。
	// 用户在 Session 中自行保留的快照及脱敏/格式化临时分配不属于该预算。
	for _, item := range []struct {
		enabled bool
		limit   int64
	}{
		{capture.RequestBody, capture.RequestBodyLimit}, {capture.ResponseBody, capture.ResponseBodyLimit},
	} {
		if item.enabled {
			if item.limit > math.MaxInt64-budget {
				return 0, tn.ErrAdmissionTooLarge
			}
			budget += item.limit
		}
	}
	return budget, nil
}
