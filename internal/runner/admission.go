package runner

import (
	"context"
	"math"
	"net/http"
	"sync/atomic"

	"github.com/dhlanshan/requests/tn"
)

type managedKey struct{}

func Managed(ctx context.Context) bool { value, _ := ctx.Value(managedKey{}).(bool); return value }

var defaultLimiter atomic.Pointer[tn.Limiter]

func init() { limiter, _ := tn.NewLimiter(64, 256<<20); defaultLimiter.Store(limiter) }

// SetDefaultLimiter 替换后续调用的共享预算；已经取得的配额仍归还到原实例。
func SetDefaultLimiter(limiter *tn.Limiter) error {
	if err := limiter.Validate(); err != nil {
		return err
	}
	defaultLimiter.Store(limiter)
	return nil
}

func bufferBudget(p tn.ApiParam, req *http.Request, retry, streaming bool) (int64, error) {
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
	return budget, nil
}
