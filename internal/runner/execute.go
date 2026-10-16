package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/dhlanshan/requests/internal/idgen"
	"github.com/dhlanshan/requests/internal/lz"
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/internal/validator"
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
	if p.LogBodyLimit == 0 {
		p.LogBodyLimit = 4 << 10
	}
	if p.EnableValid && p.Validator == nil && p.ContextValidator == nil {
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

// Execute 统一执行请求准备、配额等待、超时、重试、业务校验和安全日志。
// 每次尝试由 Client 处理重定向，最终响应只读取一次；失败时也可能返回部分 Body/Header。
// parent 控制上游取消，Timeout 控制整次调用，SingleTimeout 控制每次尝试。
// client 由上层选择默认值；调用方传入的 Body 由本次调用接管并关闭。
func Execute(parent context.Context, client *http.Client, p tn.ApiParam) (body []byte, header http.Header, err error) {
	// 先接管调用方提供的可关闭 Body，再做参数检查：即使后续提前返回，流也会被释放。
	// Guard 保证底层 Close 只执行一次，兼容取消回调、Transport 和函数退出时的重复清理。
	var owner io.ReadCloser
	if closer, ok := p.Body.(io.ReadCloser); ok {
		// 接口本身非 nil 时，内部仍可能保存一个 nil 指针，需要额外判断。
		if !utils.IsNil(closer) {
			owner = lz.Guard(closer)
			defer owner.Close()
		}
	}

	// 在准备 Body 或建立连接之前拒绝无效输入；已取消的上游请求也无需继续执行。
	if parent == nil {
		return nil, nil, errors.New("nil context")
	}
	if err = p.Check(); err != nil {
		return nil, nil, err
	}
	if err = parent.Err(); err != nil {
		return nil, nil, err
	}

	// 补齐超时、容量上限、退避和默认校验器等配置，后续阶段统一使用生效后的参数。
	// p 仅复制结构体，map、切片和 Body 仍可能共享；调用方在执行期间不应修改配置或读写该 Body。
	defaults(&p)
	// 总 Context 覆盖 Body 准备、所有尝试及重试等待；上游期限更短时自动以上游为准。
	// defer cancel 释放关联计时器，并通知仍在使用该 Context 的操作结束。
	ctx, cancel := context.WithTimeout(parent, p.Timeout)
	defer cancel()
	ctx = context.WithValue(ctx, managedKey{}, true)

	// 所有尝试共享本次调用的配置和请求总线，便于中间件读取元数据、关联日志及重试次数。
	ctx = context.WithValue(ctx, tn.CtxJKExtend{}, &p)
	bus := &tn.InternalBus{RequestId: "R" + idgen.GenKsuId()}
	if isLegacy(ctx) {
		if previous, ok := parent.Value(tn.CtxBSExtend{}).(*tn.InternalBus); ok && previous != nil {
			bus = previous
		}
	}
	ctx = context.WithValue(ctx, tn.CtxBSExtend{}, bus)

	// 构造包含方法、URL、Header 和查询参数的模板；每次尝试会基于它生成独立 Request。
	template, err := NewRequest(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	// 普通 io.Reader 可能由 NewRequest 包装成 ReadCloser，也需要负责关闭该包装流。
	// 已由 owner 接管的底层流，统一通过前面注册的清理释放。
	if template.Body != nil && owner == nil {
		defer template.Body.Close()
	}
	// 首次发送前执行调用方的地址策略；包装错误后可用 errors.Is 判断策略拒绝及原始原因。
	// 后续重定向的地址检查由 policyClient 接入 http.Client 的 CheckRedirect 流程。
	if p.URLPolicy != nil {
		copyURL := *template.URL
		if err := p.URLPolicy(ctx, &copyURL); err != nil {
			return nil, nil, &tn.RequestError{Err: fmt.Errorf("%w: %w", tn.ErrTargetBlocked, err)}
		}
	}

	// attempts 统计实际进入 HTTP 执行阶段的尝试数；status 保存最近一次尝试的状态码。
	// 从这里开始记录 Body 准备及后续执行耗时；captured 按需保存首次发送的有界日志预览。
	attempts, status := 0, 0
	started := time.Now()
	var captured *captureBody
	// 命名返回值会在 defer 执行前被赋值，因此这里能够记录最终返回的 Body、Header 和错误。
	defer func() {
		// 总线沿用“额外重试次数”的含义；Body 准备失败、尚未发送时记为 0。
		retries := attempts - 1
		if retries < 0 {
			retries = 0
		}
		bus.MdwDataMap.Store("Retry Middleware", &tn.RetryData{TryCnt: retries})
		// 日志开关关闭时直接跳过格式化；开启后由 logResult 统一做元数据脱敏和预览限制。
		if p.EchoReq || p.EchoRes {
			logResult(p, template, bus.RequestId, started, attempts, status, body, header, captured, err)
		}
	}()

	// 先判断业务操作是否允许重试：GET/HEAD/OPTIONS，或显式允许，或带非空幂等键。
	// 允许重试不代表所有错误都重试，具体失败原因还需要由每次执行结果判断。
	retryAllowed := canRetry(template, p)
	// 在预读一次性流前等待共享配额，避免大量等待连接的请求先占满正文缓存。
	budget, budgetErr := bufferBudget(p, template, retryAllowed, streamTargetFrom(ctx) != nil)
	if budgetErr != nil {
		return nil, nil, budgetErr
	}
	limiter := p.Limiter
	if limiter == nil {
		limiter = defaultLimiter.Load()
	}
	release, admissionErr := limiter.Acquire(ctx, budget)
	if admissionErr != nil {
		return nil, nil, admissionErr
	}
	defer release()
	// 建立 Body 重放方案：可重建流按需重开；需要重试的一次性流才进入有上限的缓存。
	// 读取或缓存失败时直接结束，避免发送缺少数据的请求。
	plan, err := prepareBody(ctx, template, p, owner, p.Retry > 0 && retryAllowed)
	if err != nil {
		return nil, nil, err
	}
	// 为本次调用复制 Client 并叠加重定向策略，同时复用原 Transport、连接池和 CookieJar。
	client = policyClient(client, p, template.URL, &retryAllowed)

	// Retry 表示首次之外的额外次数，因此循环最多执行 Retry+1 次；attempt 从 0 开始。
	for attempt := 0; attempt <= p.Retry; attempt++ {
		// 总期限或上游取消优先；结束时保留上一轮响应数据，供调用方诊断失败原因。
		if err = ctx.Err(); err != nil {
			return body, header, err
		}
		// 每次使用独立子 Context，覆盖打开 Body、网络交互、响应读取和业务校验。
		// 单次超时只结束本次尝试；总 Context 仍有效时，可以继续下一次重试。
		attemptCtx, attemptCancel := context.WithTimeout(ctx, p.SingleTimeout)
		// 克隆请求模板并打开本次 Body；可重放时同时设置 GetBody，供 307/308 重定向使用。
		req, openErr := plan.request(attemptCtx, template)
		if openErr != nil {
			// Body 打开过程若已超过期限，优先返回 Context 错误，便于统一判断取消或超时。
			if attemptCtx.Err() != nil {
				openErr = attemptCtx.Err()
			}
			attemptCancel()
			return body, header, openErr
		}

		// 仅在明确开启请求正文日志时，随首次发送捕获有限前缀；这里不提前读取上传流。
		// 重试继续使用重放方案生成的 Body，日志保留首次尝试的预览。
		if attempt == 0 && p.EchoReq && p.LogBody && logEnabled(attemptCtx, p) && req.Body != http.NoBody {
			captured = &captureBody{ReadCloser: req.Body, limit: p.LogBodyLimit}
			req.Body = captured
		}

		// 发送前更新统计，让本次执行中的中间件能够读到当前重试序号。
		attempts++
		bus.MdwDataMap.Store("Retry Middleware", &tn.RetryData{TryCnt: attempt})
		var retry bool
		// perform 完成 Client.Do、重定向、最终响应的有界读取与关闭，以及可选业务校验。
		// retry 只说明本次失败是否适合重试，还需结合方法安全性、次数及总期限作最终决策。
		body, header, status, retry, err = perform(client, req, p)
		// 本次尝试结束立即关闭上传流；Close 的返回值不覆盖网络、读取或业务校验的执行结果。
		_ = req.Body.Close()
		// 对已知长度的 Body，读满声明长度也能确认预览完整，兼容没有额外读取 EOF 的发送实现。
		if captured != nil && attempt == 0 && req.ContentLength > 0 {
			captured.markLength(req.ContentLength)
		}
		// 在读取和校验之后再次检查单次期限，避免超时回调较晚返回时被误判为成功。
		// 单次 DeadlineExceeded 可被视为临时失败，是否继续仍由下面的总期限检查决定。
		if attemptCtx.Err() != nil {
			err = attemptCtx.Err()
			retry = retryError(err)
		}
		// 每轮及时取消子 Context，释放计时器；在循环里 defer 会把清理推迟到整个调用结束。
		attemptCancel()

		// 总期限或上游取消一旦生效，立即停止；单次超时不能使请求突破整次调用的期限。
		if ctx.Err() != nil {
			return body, header, ctx.Err()
		}
		// 成功、次数耗尽、方法不允许重试、失败原因不可重试，任一成立都直接返回最近结果。
		if err == nil || attempt == p.Retry || !retryAllowed || retryForbidden(err) {
			return body, header, err
		}
		// 流式输出一旦写入任何字节便不能重新请求并拼接另一份响应。
		if target := streamTargetFrom(ctx); target != nil && target.written > 0 {
			return body, header, err
		}
		if p.RetryDecider != nil {
			retry = p.RetryDecider(ctx, tn.RetryInfo{Attempt: attempt + 1, StatusCode: status, Header: header.Clone(), Err: err, DefaultRetry: retry})
		}
		if !retry {
			return body, header, err
		}
		// retryDelay 结合自定义/固定/指数退避和 Retry-After 计算有上限的等待时间。
		// 等待使用总 Context，使取消能及时打断；重试序号从 1 开始传入退避函数。
		if waitErr := waitRetry(ctx, retryDelay(p, attempt+1, header)); waitErr != nil {
			return body, header, waitErr
		}
	}
	return body, header, err
}

func perform(client *http.Client, req *http.Request, p tn.ApiParam) ([]byte, http.Header, int, bool, error) {
	resp, err := client.Do(req)
	if err != nil {
		// CheckRedirect 拒绝时 net/http 已关闭 Body，但 Header 仍有诊断价值。
		var header http.Header
		var status int
		if resp != nil {
			header, status = resp.Header.Clone(), resp.StatusCode
		}
		return nil, header, status, retryError(err), &tn.RequestError{Err: err}
	}
	header := resp.Header.Clone()
	resp.Body = lz.Guard(resp.Body)
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
		return nil, header, resp.StatusCode, n == 0 && retryError(copyErr), copyErr
	}
	body, readErr := lz.ReadBounded(req.Context(), resp.Body, limit)
	if !success {
		httpErr := &tn.HTTPError{StatusCode: resp.StatusCode, Header: header, Body: body, Truncated: errors.Is(readErr, tn.ErrResponseTooLarge)}
		err = httpErr
		if readErr != nil {
			err = errors.Join(httpErr, readErr)
		}
		return body, header, resp.StatusCode, !httpErr.Truncated && (retryStatus(resp.StatusCode, p) || retryError(readErr)), err
	}
	if readErr != nil {
		return body, header, resp.StatusCode, retryError(readErr), fmt.Errorf("read response: %w", readErr)
	}
	if p.EnableValid && !(isLegacy(req.Context()) && resp.StatusCode >= 300 && resp.StatusCode <= 399) {
		_, err = validator.Validate(req.Context(), p, body, header)
		if err != nil {
			return body, header, resp.StatusCode, p.RetryValidation && !retryForbidden(err), err
		}
	}
	return body, header, resp.StatusCode, false, nil
}
