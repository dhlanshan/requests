package tn

import (
	"context"
	"errors"
	"sync"
)

// Limiter 显式限制使用同一实例的调用数量和正文缓冲预算，所有方法支持并发调用。
// ApiParam.Limiter 为 nil 时关闭限制；同一指针用于一组调用，不同实例分别计数。
// 一个调用取得名额后，正文准备、各轮执行及重试等待都共用该名额，退出清理时归还。
// 预算按配置上限预留；Go 扩容、用户回调和调用结束后继续保留的结果不在预算内。
// 配置在创建后固定，实例开始使用后不能复制。
type Limiter struct {
	mu            sync.Mutex
	maxConcurrent int
	maxBuffered   int64
	active        int
	buffered      int64
	changed       chan struct{}
}

// NewLimiter 创建一组调用共用的限制，创建后由使用者显式赋给 ApiParam.Limiter。
// maxConcurrent 必须为正数；maxBufferedBytes=0 只限制调用数，正值同时启用容量预算。
// 应在业务初始化时创建并复用；每个调用各自新建实例无法限制这些调用的合计数量。
func NewLimiter(maxConcurrent int, maxBufferedBytes int64) (*Limiter, error) {
	if maxConcurrent <= 0 || maxBufferedBytes < 0 {
		return nil, errors.New("invalid limiter capacity")
	}
	return &Limiter{maxConcurrent: maxConcurrent, maxBuffered: maxBufferedBytes, changed: make(chan struct{})}, nil
}

// MaxBufferedBytes 返回创建时设置的正文容量预算上限；0 表示关闭这项限制。
// nil 接收者也返回 0。配置创建后不变，因此查询不需要竞争运行时计数锁。
// 执行层据此在只限制并发时跳过预算计算，避免正文上限产生额外的容量拒绝。
func (l *Limiter) MaxBufferedBytes() int64 {
	if l == nil {
		return 0
	}
	return l.maxBuffered
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
// bytes 是调用方估算的正文预算；MaxBufferedBytes 为 0 时不统计该值，可传 0。
// 多个等待者在有名额时竞争取得名额，不保证严格按到达顺序放行。
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
