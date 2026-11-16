package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/dhlanshan/requests/internal/lz"
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/internal/validator"
	"github.com/dhlanshan/requests/logging"
	"github.com/dhlanshan/requests/tn"
)

func defaults(p *tn.ApiParam) {
	if p.MaxRedirects == 0 {
		p.MaxRedirects = 10
	}
	if p.SingleTimeout == 0 {
		p.SingleTimeout = 30 * time.Second
	}
	if p.MaxRetryDelay == 0 {
		p.MaxRetryDelay = 30 * time.Second
	}
	if p.MaxResponseBytes == 0 {
		p.MaxResponseBytes = lz.DefaultResponseLimit
	}
	if p.MaxErrorBytes == 0 {
		p.MaxErrorBytes = 64 << 10
	}
	if p.MaxReplayBytes == 0 {
		p.MaxReplayBytes = 8 << 20
	}
	if p.EnableValid && p.Validator == nil {
		p.Validator = validator.DefaultValidator
	}
	if p.Timeout == 0 {
		// 饱和加法避免 Duration 乘加溢出后得到负数，导致立即超时。
		add := func(value time.Duration) {
			if value > time.Duration(math.MaxInt64)-p.Timeout {
				p.Timeout = time.Duration(math.MaxInt64)
			} else {
				p.Timeout += value
			}
		}
		for i := 0; i <= p.Retry; i++ {
			add(p.SingleTimeout)
		}
		interval := p.RetryInterval
		if interval == 0 || p.Backoff != nil {
			interval = p.MaxRetryDelay
		}
		if interval > p.MaxRetryDelay {
			interval = p.MaxRetryDelay
		}
		for i := 0; i < p.Retry; i++ {
			add(interval)
		}
	}
}

// Execute 统一执行请求准备、配额等待、超时、重试、业务校验及可替换日志。
// 一次调用可以有多次尝试；日志按整次调用和每次尝试分别派发，不使用 Transport 中间件。
// parent 控制上游取消，Timeout 控制执行总期限，SingleTimeout 控制每次尝试。
// client 由上层选择默认值；调用方传入的 Body 由本次调用接管并关闭。
func Execute(parent context.Context, client *http.Client, p tn.ApiParam) (body []byte, header http.Header, err error) {
	return execute(parent, client, p, time.Now())
}

// execute 的 started 由入口传入，使流式入口的前置检查也能使用同一计时口径。
// 日志结束 defer 最先注册、最后执行：资源清理先完成，再确定耗时和派发 OnEnd。
func execute(parent context.Context, client *http.Client, p tn.ApiParam, started time.Time) (body []byte, header http.Header, err error) {
	var call *logging.Call
	var limiterWaitDuration time.Duration
	attempts, status := 0, 0
	phase := logging.PhaseParameters
	defer func() {
		retries := attempts - 1
		if retries < 0 {
			retries = 0
		}
		if err == nil {
			phase = logging.PhaseComplete
		}
		call.OnEnd(logContext(parent), logging.CallResult{
			AttemptCount: attempts, RetryCount: retries, Duration: time.Since(started),
			LimiterWaitDuration: limiterWaitDuration,
			StatusCode:          status, Phase: phase, Err: err,
		})
	}()

	// 先接管可关闭 Body，再调用任何用户日志扩展；即使提前返回也释放该流。
	// Guard 保证 Transport、取消回调和本函数重复清理只关闭底层一次。
	var owner io.ReadCloser
	if closer, ok := p.Body.(io.ReadCloser); ok && !utils.IsNil(closer) {
		owner = lz.Guard(closer)
		defer owner.Close()
	}
	call = startCallLogging(parent, p, started)

	if parent == nil {
		return nil, nil, errors.New("nil context")
	}
	// 日志已经开启，因此参数无效或上游已取消时，也能收到 attempts=0 的结束事件。
	// 所有检查均发生在分配连接和读取上传正文之前。
	if err = p.Check(); err != nil {
		return nil, nil, err
	}
	if err = parent.Err(); err != nil {
		return nil, nil, err
	}

	// p 仅复制结构体，map、切片、日志实现和 Body 仍可能共享；调用期间配置应只读。
	defaults(&p)
	// 以执行入口为总期限起点，避免同步 Start 回调消耗的时间被排除在 Timeout 外。
	// 回调本身无法强制终止，但返回后会立即按这个期限判断是否还能继续请求。
	ctx, cancel := context.WithDeadline(parent, started.Add(p.Timeout))
	defer cancel()

	// 模板保存方法、地址、Header 和查询参数；每次尝试再克隆模板并打开自己的 Body。
	phase = logging.PhaseRequest
	template, err := NewRequest(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	if template.Body != nil && owner == nil {
		// 没有底层 owner 时，NewRequest 包装出来的流也由本次调用负责释放。
		defer template.Body.Close()
	}
	// 初始地址在这里检查；后续每次重定向的检查由 policyClient 接入 CheckRedirect。
	phase = logging.PhasePolicy
	if p.URLPolicy != nil {
		copyURL := *template.URL
		if err := p.URLPolicy(ctx, &copyURL); err != nil {
			return nil, nil, &tn.RequestError{Err: fmt.Errorf("%w: %w", tn.ErrTargetBlocked, err)}
		}
	}

	retryAllowed := canRetry(template, p)
	// 默认路径只有一次 nil 判断，不计算共享预算，也不竞争 Limiter 的锁或等待名额。
	// 使用者显式设置实例后才启用；同一调用的所有尝试和重试等待共用一次取得的名额。
	if limiter := p.Limiter; limiter != nil {
		phase = logging.PhaseAdmission
		var budget int64
		if limiter.MaxBufferedBytes() > 0 {
			// 仅在明确启用容量限制时计算，避免只限制并发的调用因预算相加溢出而被拒绝。
			var budgetErr error
			budget, budgetErr = bufferBudget(p, template, retryAllowed, streamTargetFrom(ctx) != nil, call.Capture())
			if budgetErr != nil {
				return nil, nil, budgetErr
			}
		}
		waitStarted := time.Now()
		release, admissionErr := limiter.Acquire(ctx, budget)
		// 成功或失败均记录 Acquire 的实际耗时，包括检查开销；未启用时保持精确的 0。
		limiterWaitDuration = time.Since(waitStarted)
		if admissionErr != nil {
			return nil, nil, admissionErr
		}
		defer release()
	}

	phase = logging.PhaseBodyPreparation
	plan, err := prepareBody(ctx, template, p, owner, p.Retry > 0 && retryAllowed)
	if err != nil {
		return nil, nil, err
	}
	// 复制 Client 的策略配置，继续共享原 Transport、连接池和 CookieJar。
	client = policyClient(client, p, template.URL, &retryAllowed)

	// Retry 是额外次数。一次尝试包括为该次尝试打开正文；只有真正进入下一轮才增加计数。
	// 重试计划已派发但等待期间取消时，不会增加实际 RetryCount。
	for attempt := 0; attempt <= p.Retry; attempt++ {
		if err = ctx.Err(); err != nil {
			return body, header, err
		}
		attemptStarted := time.Now()
		attempts++
		record := newAttemptLog(call, template, attempts, attemptStarted)
		phase = logging.PhaseBodyOpen
		// 每轮独立创建并及时取消 Context，SingleTimeout 同时覆盖打开 Body、发送、读取和校验。
		attemptCtx, attemptCancel := context.WithTimeout(ctx, p.SingleTimeout)
		req, openErr := plan.request(attemptCtx, template)
		if openErr != nil {
			if attemptCtx.Err() != nil {
				openErr = attemptCtx.Err()
			}
			attemptCancel()
			record.end(parent, phase, openErr)
			return body, header, openErr
		}
		record.attachRequest(req)

		// perform 包括响应正文读取及业务验证；不能在 Client.Do 刚返回时就宣告尝试完成。
		var retry bool
		body, header, status, retry, phase, err = perform(client, req, p, record)
		_ = req.Body.Close()
		// 回调或自定义 Transport 可能在超时后才返回；重新判定以免将迟到的结果记作成功。
		if attemptCtx.Err() != nil {
			err = attemptCtx.Err()
			retry = retryError(err)
			if phase == logging.PhaseComplete {
				phase = logging.PhaseAttempt
			}
		}
		attemptCancel()
		if ctx.Err() != nil {
			err = ctx.Err()
			if phase == logging.PhaseComplete {
				phase = logging.PhaseAttempt
			}
		}
		record.end(parent, phase, err)

		// 日志是同步扩展，回调自身也可能消耗时间；派发后仍须复核总期限。
		if ctx.Err() != nil {
			if phase == logging.PhaseComplete {
				phase = logging.PhaseAttempt
			}
			return body, header, ctx.Err()
		}
		if err == nil || attempt == p.Retry || !retryAllowed || retryForbidden(err) {
			return body, header, err
		}
		if target := streamTargetFrom(ctx); target != nil && target.written > 0 {
			// 已写出的成功正文不能与另一次请求拼接，因此开始输出后禁止重试。
			return body, header, err
		}
		if p.RetryDecider != nil {
			retry = p.RetryDecider(ctx, tn.RetryInfo{Attempt: attempt + 1, StatusCode: status, Header: header.Clone(), Err: err, DefaultRetry: retry})
		}
		if !retry {
			return body, header, err
		}
		// 退避只计算一次，确保日志中的计划等待时间就是执行层实际采用的间隔。
		delay := retryDelay(p, attempt+1, header)
		call.OnRetry(logContext(parent), logging.RetryEvent{
			Attempt: attempts, NextAttempt: attempts + 1, Delay: delay,
			StatusCode: status, Phase: phase, Err: err,
		})
		phase = logging.PhaseRetryWait
		if waitErr := waitRetry(ctx, delay); waitErr != nil {
			return body, header, waitErr
		}
	}
	return body, header, err
}

func perform(client *http.Client, req *http.Request, p tn.ApiParam, record *attemptLog) ([]byte, http.Header, int, bool, logging.Phase, error) {
	resp, err := client.Do(req)
	if err != nil {
		var header http.Header
		var status int
		if resp != nil {
			header, status = resp.Header.Clone(), resp.StatusCode
			// CheckRedirect 拒绝时正文已被 net/http 关闭，只采集现有元数据，不再读取。
			record.attachResponse(req, resp)
		}
		return nil, header, status, retryError(err), logging.PhaseSend, &tn.RequestError{Err: err}
	}
	header := resp.Header.Clone()
	resp.Body = lz.Guard(resp.Body)
	record.attachResponse(req, resp)
	defer resp.Body.Close()
	success := resp.StatusCode >= 200 && resp.StatusCode <= 299
	if p.SuccessStatus != nil {
		success = p.SuccessStatus(resp.StatusCode)
	}
	limit := p.MaxResponseBytes
	if !success && p.MaxErrorBytes < limit {
		limit = p.MaxErrorBytes
	}
	if target := streamTargetFrom(req.Context()); target != nil && success {
		n, copyErr := lz.CopyBounded(req.Context(), target.writer, resp.Body, limit)
		target.written += n
		phase := logging.PhaseComplete
		if copyErr != nil {
			phase = logging.PhaseResponseRead
			if errors.Is(copyErr, tn.ErrResponseWrite) {
				phase = logging.PhaseResponseWrite
			}
		}
		return nil, header, resp.StatusCode, n == 0 && retryError(copyErr), phase, copyErr
	}
	body, readErr := lz.ReadBounded(req.Context(), resp.Body, limit)
	if !success {
		httpErr := &tn.HTTPError{StatusCode: resp.StatusCode, Header: header, Body: body, Truncated: errors.Is(readErr, tn.ErrResponseTooLarge)}
		err = httpErr
		phase := logging.PhaseStatus
		if readErr != nil {
			err = errors.Join(httpErr, readErr)
			phase = logging.PhaseResponseRead
		}
		return body, header, resp.StatusCode, !httpErr.Truncated && (retryStatus(resp.StatusCode, p) || retryError(readErr)), phase, err
	}
	if readErr != nil {
		return body, header, resp.StatusCode, retryError(readErr), logging.PhaseResponseRead, fmt.Errorf("read response: %w", readErr)
	}
	if p.EnableValid {
		_, err = validator.Validate(req.Context(), p.Validator, body, header)
		if err != nil {
			return body, header, resp.StatusCode, p.RetryValidation && !retryForbidden(err), logging.PhaseValidation, err
		}
	}
	return body, header, resp.StatusCode, false, logging.PhaseComplete, nil
}
