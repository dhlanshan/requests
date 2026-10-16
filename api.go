package requests

import (
	"context"
	"io"
	"net/http"

	"github.com/dhlanshan/requests/internal/core"
	"github.com/dhlanshan/requests/internal/runner"
	"github.com/dhlanshan/requests/tn"
)

// Api 保留原有入口；需要取消或继承上游期限时使用 ApiContext。
// 失败时仍返回已读取的有界 Body 和 Header，便于处理业务错误响应。
func Api(client *http.Client, p tn.ApiParam) ([]byte, http.Header, error) {
	return ApiContext(context.Background(), client, p)
}

// ApiContext 统一执行超时、重试、业务验证及安全日志，适用于任意 Client。
func ApiContext(ctx context.Context, client *http.Client, p tn.ApiParam) ([]byte, http.Header, error) {
	if client == nil {
		client = core.NewDefaultClient()
	}
	return runner.Execute(ctx, client, p)
}

// ApiTo 将成功响应流式写入 dst，返回实际写入字节数；不支持依赖完整正文的业务校验。
// 失败时 dst 可能已有部分数据；一旦发生写入便不自动重试，调用方负责文件/事务的提交或回滚。
func ApiTo(ctx context.Context, client *http.Client, p tn.ApiParam, dst io.Writer) (int64, http.Header, error) {
	if client == nil {
		client = core.NewDefaultClient()
	}
	return runner.ExecuteTo(ctx, client, p, dst)
}

// SetDefaultLimiter 调整后续调用的默认共享预算；单个调用的 Limiter 字段可指定业务共享预算。
func SetDefaultLimiter(limiter *tn.Limiter) error { return runner.SetDefaultLimiter(limiter) }
