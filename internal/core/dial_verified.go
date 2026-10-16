package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

type dialResult struct {
	conn     net.Conn
	err      error
	fallback bool
}

// dialVerified 只接受已经过策略校验的 IP，双栈竞速不重新解析域名。
// 最多两条串行候选链并发；胜者返回后取消另一条链，迟到的连接立即关闭。
func dialVerified(ctx context.Context, network, port string, addresses []netip.Addr, delay time.Duration, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	var primary, fallback []netip.Addr
	for _, addr := range addresses {
		addr = addr.Unmap()
		if network == "tcp4" && !addr.Is4() || network == "tcp6" && addr.Is4() {
			continue
		}
		if len(primary) == 0 || addr.Is4() == primary[0].Is4() {
			primary = append(primary, addr)
		} else {
			fallback = append(fallback, addr)
		}
	}
	if len(primary) == 0 {
		return nil, errors.New("no address matches dial network")
	}
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan dialResult)
	start := func(list []netip.Addr, isFallback bool) {
		go func() {
			var conn net.Conn
			var err error
			for i, addr := range list {
				if raceCtx.Err() != nil {
					err = raceCtx.Err()
					break
				}
				budget := 30 * time.Second
				if deadline, ok := raceCtx.Deadline(); ok && time.Until(deadline) < budget {
					budget = time.Until(deadline)
				}
				attemptCtx, done := context.WithTimeout(raceCtx, budget/time.Duration(len(list)-i))
				conn, err = dial(attemptCtx, network, net.JoinHostPort(addr.String(), port))
				done()
				if err == nil {
					break
				}
			}
			select {
			case results <- dialResult{conn: conn, err: err, fallback: isFallback}:
			case <-raceCtx.Done():
				if conn != nil {
					_ = conn.Close()
				}
			}
		}()
	}
	start(primary, false)
	var timer *time.Timer
	var tick <-chan time.Time
	if len(fallback) > 0 {
		timer = time.NewTimer(delay)
		defer timer.Stop()
		tick = timer.C
	}
	startedFallback := false
	finished := 0
	var failures []error
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick:
			start(fallback, true)
			startedFallback = true
			tick = nil
		case result := <-results:
			if result.err == nil {
				return result.conn, nil
			}
			failures = append(failures, result.err)
			finished++
			if !result.fallback && !startedFallback && len(fallback) > 0 {
				start(fallback, true)
				startedFallback = true
				tick = nil
			}
			want := 1
			if len(fallback) > 0 {
				want = 2
			}
			if finished == want {
				return nil, errors.Join(failures...)
			}
		}
	}
}
