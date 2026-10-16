package tn

import (
	"context"
	"errors"
	"sync"
)

// Limiter 限制使用同一实例的在途调用数和正文缓冲预算，所有方法支持并发调用。
// 预算衡量正文保留容量；Go 扩容、用户回调和调用结束后继续保留的结果不在预算内。
type Limiter struct {
	mu            sync.Mutex
	maxConcurrent int
	maxBuffered   int64
	active        int
	buffered      int64
	changed       chan struct{}
}

// NewLimiter 创建共享预算；maxConcurrent 必须为正数，maxBufferedBytes=0 只限制调用数。
func NewLimiter(maxConcurrent int, maxBufferedBytes int64) (*Limiter, error) {
	if maxConcurrent <= 0 || maxBufferedBytes < 0 {
		return nil, errors.New("invalid limiter capacity")
	}
	return &Limiter{maxConcurrent: maxConcurrent, maxBuffered: maxBufferedBytes, changed: make(chan struct{})}, nil
}

// Validate 检查是否为有效的构造器实例。
func (l *Limiter) Validate() error {
	if l == nil {
		return errors.New("nil limiter")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.maxConcurrent <= 0 || l.maxBuffered < 0 || l.changed == nil {
		return errors.New("limiter must be created with NewLimiter")
	}
	return nil
}

// Acquire 在读取或缓存 Body 前排队，Context 到期立即退出，不启动等待 goroutine。
// 单次预算超过总容量时直接失败；成功后必须调用 release，重复 release 是安全的。
func (l *Limiter) Acquire(ctx context.Context, bytes int64) (release func(), err error) {
	if l == nil || ctx == nil || bytes < 0 {
		return nil, errors.New("invalid limiter acquisition")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		l.mu.Lock()
		if l.maxConcurrent <= 0 {
			l.mu.Unlock()
			return nil, errors.New("limiter must be created with NewLimiter")
		}
		if l.maxBuffered > 0 && bytes > l.maxBuffered {
			l.mu.Unlock()
			return nil, ErrAdmissionTooLarge
		}
		if l.active < l.maxConcurrent && (l.maxBuffered == 0 || bytes <= l.maxBuffered-l.buffered) {
			l.active++
			if l.maxBuffered > 0 {
				l.buffered += bytes
			}
			l.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					l.mu.Lock()
					l.active--
					if l.maxBuffered > 0 {
						l.buffered -= bytes
					}
					close(l.changed)
					l.changed = make(chan struct{})
					l.mu.Unlock()
				})
			}, nil
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}
