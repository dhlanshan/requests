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

// callExecution 只保存一次调用的观察状态；不在并发调用之间共享。
type callExecution struct {
	call                   *logging.Call
	limiterWaitDuration    time.Duration
	attempts               int
	lastResponseStatusCode int
	phase                  logging.Phase
	err                    error
}

// execute 的 started 由入口传入，使流式入口的前置检查也能使用同一计时口径。
// 主体返回并完成清理之后才标记正常结束；panic 和 Goexit 均不能被误记为成功。
// 此处不 recover，也不保存 panic 值，保持调用方原有的异常传播和恢复行为。
func execute(parent context.Context, client *http.Client, p tn.ApiParam, started time.Time) (body []byte, header http.Header, err error) {
	run := callExecution{phase: logging.PhaseParameters}
	returnedNormally := false
	defer func() {
		endErr := err
		if !returnedNormally {
			run.phase = logging.PhaseAborted
			endErr = errors.Join(run.err, tn.ErrExecutionAborted)
		} else if err == nil {
			run.phase = logging.PhaseComplete
		}
		retries := max(run.attempts-1, 0)
		run.call.OnEnd(logContext(parent), logging.CallResult{
			AttemptCount: run.attempts, RetryCount: retries, Duration: time.Since(started),
			LimiterWaitDuration: run.limiterWaitDuration,
			StatusCode:          run.lastResponseStatusCode, Phase: run.phase, Err: endErr,
		})
	}()
	body, header, err = run.execute(parent, client, p, started)
	returnedNormally = true
	return
}

func (run *callExecution) execute(parent context.Context, client *http.Client, p tn.ApiParam, started time.Time) (body []byte, header http.Header, err error) {
	// 在资源清理之后保存当前失败，供异常退出的最终日志使用；不保存 panic 的值。
	defer func() { run.err = err }()

	// 先接管可关闭 Body，再调用任何用户日志扩展；即使提前返回也释放该流。
	// Guard 保证 Transport、取消回调和本函数重复清理只关闭底层一次。
	var owner io.ReadCloser
	if closer, ok := p.Body.(io.ReadCloser); ok && !utils.IsNil(closer) {
		owner = lz.Guard(closer)
		defer owner.Close()
	}
	run.call = startCallLogging(parent, p, started)

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
	run.phase = logging.PhaseRequest
	template, err := NewRequest(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	if template.Body != nil && owner == nil {
		// 没有底层 owner 时，NewRequest 包装出来的流也由本次调用负责释放。
		defer template.Body.Close()
	}
	// 初始地址在这里检查；后续每次重定向的检查由 policyClient 接入 CheckRedirect。
	run.phase = logging.PhasePolicy
	if p.URLPolicy != nil {
		copyURL := *template.URL
		policyErr := p.URLPolicy(ctx, &copyURL)
		if policyErr != nil {
			policyErr = fmt.Errorf("%w: %w", tn.ErrTargetBlocked, policyErr)
		}
		// 回调可以在取消或总期限到达后才返回，包含返回 nil 的情况。
		if policyErr = utils.MergeContextError(policyErr, ctx.Err()); policyErr != nil {
			return nil, nil, &tn.RequestError{Err: policyErr}
		}
	}

	retryAllowed := canRetry(template, p)
	// 默认路径只有一次 nil 判断，不计算共享预算，也不竞争 Limiter 的锁或等待名额。
	// 使用者显式设置实例后才启用；同一调用的所有尝试和重试等待共用一次取得的名额。
	if limiter := p.Limiter; limiter != nil {
		run.phase = logging.PhaseAdmission
		var budget int64
		if limiter.MaxBufferedBytes() > 0 {
			// 仅在明确启用容量限制时计算，避免只限制并发的调用因预算相加溢出而被拒绝。
			var budgetErr error
			budget, budgetErr = bufferBudget(p, template, retryAllowed, streamTargetFrom(ctx) != nil, run.call.Capture())
			if budgetErr != nil {
				return nil, nil, budgetErr
			}
		}
		waitStarted := time.Now()
		release, admissionErr := limiter.Acquire(ctx, budget)
		// 成功或失败均记录 Acquire 的实际耗时，包括检查开销；未启用时保持精确的 0。
		run.limiterWaitDuration = time.Since(waitStarted)
		if admissionErr != nil {
			return nil, nil, admissionErr
		}
		defer release()
	}

	run.phase = logging.PhaseBodyPreparation
	plan, err := prepareBody(ctx, template, p, owner, p.Retry > 0 && retryAllowed)
	err = utils.MergeContextError(err, ctx.Err())
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
		run.attempts++
		result := executeAttempt(parent, ctx, client, p, plan, template, run.call, run.attempts, attemptStarted, &run.lastResponseStatusCode)
		run.phase, err = result.phase, result.err
		if result.opened {
			body, header = result.body, result.header
		}
		retry, attemptStatusCode := result.retry, result.status

		// 日志是同步扩展，回调自身也可能消耗时间；派发后仍须复核总期限。
		if ctxErr := ctx.Err(); ctxErr != nil {
			if run.phase == logging.PhaseComplete {
				run.phase = logging.PhaseAttempt
			}
			return body, header, utils.MergeContextError(err, ctxErr)
		}
		if !result.opened {
			return body, header, err
		}
		if err == nil || attempt == p.Retry || !retryAllowed || retryForbidden(err) {
			return body, header, err
		}
		if target := streamTargetFrom(ctx); target != nil && target.written > 0 {
			// 已写出的成功正文不能与另一次请求拼接，因此开始输出后禁止重试。
			return body, header, err
		}
		if p.RetryDecider != nil {
			retry = p.RetryDecider(ctx, tn.RetryInfo{Attempt: attempt + 1, StatusCode: attemptStatusCode, Header: header.Clone(), Err: err, DefaultRetry: retry})
			// false 也是出口，不能只依赖进入重试等待时才观察取消。
			if ctxErr := ctx.Err(); ctxErr != nil {
				return body, header, utils.MergeContextError(err, ctxErr)
			}
		}
		if !retry {
			return body, header, err
		}
		// 退避只计算一次，确保日志中的计划等待时间就是执行层实际采用的间隔。
		delay := retryDelay(p, attempt+1, header)
		run.call.OnRetry(logContext(parent), logging.RetryEvent{
			Attempt: run.attempts, NextAttempt: run.attempts + 1, Delay: delay,
			StatusCode: attemptStatusCode, Phase: run.phase, Err: err,
		})
		run.phase = logging.PhaseRetryWait
		if waitErr := waitRetry(ctx, delay); waitErr != nil {
			return body, header, waitErr
		}
	}
	return body, header, err
}

// attemptResult 属于当前轮，不会把上轮的状态码或错误带入重试决策。
type attemptResult struct {
	body   []byte
	header http.Header
	status int
	retry  bool
	phase  logging.Phase
	err    error
	opened bool
}

// executeAttempt 的结束观察在 performAttempt 的全部清理之后执行。
// 只有主体正常返回才使用正常阶段；异常退出也为已开始的尝试派发一次结束事件。
func executeAttempt(parent, ctx context.Context, client *http.Client, p tn.ApiParam, plan bodyPlan, template *http.Request, call *logging.Call, number int, started time.Time, lastResponseStatusCode *int) (result attemptResult) {
	result.phase = logging.PhaseBodyOpen
	record := newAttemptLog(call, template, number, started)
	returnedNormally := false
	defer func() {
		if !returnedNormally {
			result.phase = logging.PhaseAborted
			result.err = errors.Join(result.err, tn.ErrExecutionAborted)
		}
		record.end(parent, result.phase, result.err)
	}()
	performAttempt(ctx, client, p, plan, template, record, lastResponseStatusCode, &result)
	returnedNormally = true
	return
}

// performAttempt 用函数作用域管理当前轮的 Context 和上传流。
// 两个 defer 紧随资源接管注册；即使工厂、Transport 或业务回调 panic 也执行清理。
func performAttempt(ctx context.Context, client *http.Client, p tn.ApiParam, plan bodyPlan, template *http.Request, record *attemptLog, lastResponseStatusCode *int, result *attemptResult) {
	attemptCtx, attemptCancel := context.WithTimeout(ctx, p.SingleTimeout)
	defer attemptCancel()
	req, openErr := plan.request(attemptCtx, template)
	if openErr != nil {
		result.err = utils.MergeContextError(openErr, attemptCtx.Err())
		result.err = utils.MergeContextError(result.err, ctx.Err())
		return
	}
	result.opened = true
	defer req.Body.Close()
	record.attachRequest(req)
	result.body, result.header, result.status, result.retry, result.phase, result.err = perform(client, req, p, record, lastResponseStatusCode)
	_ = req.Body.Close()
	// 主动 cancel 之前读取期限；保留原始禁止重试标记，再按合并后的错误分类。
	if ctxErr := attemptCtx.Err(); ctxErr != nil {
		result.err = utils.MergeContextError(result.err, ctxErr)
		result.retry = retryError(result.err)
		if result.phase == logging.PhaseComplete {
			result.phase = logging.PhaseAttempt
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		result.err = utils.MergeContextError(result.err, ctxErr)
		if result.phase == logging.PhaseComplete {
			result.phase = logging.PhaseAttempt
		}
	}
}

func perform(client *http.Client, req *http.Request, p tn.ApiParam, record *attemptLog, lastResponseStatusCode *int) ([]byte, http.Header, int, bool, logging.Phase, error) {
	resp, err := client.Do(req)
	// 取得响应后立即保存汇总元数据，后面的扩展即使异常退出也不会抹掉该状态。
	if resp != nil && resp.StatusCode > 0 {
		*lastResponseStatusCode = resp.StatusCode
	}
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
