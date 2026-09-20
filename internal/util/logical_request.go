package util

import "context"

type logicalRequestContextKey struct{}

// WithLogicalRequestLifetime 标记整次 handler 请求或独立 Manager 操作的生命周期。它只传递完成
// 信号，不改变 session 归属；attempt/lane 的子 context 取消不会结束该信号。
// 流式调用必须在交付通道结束后调用 finish，而不是返回 StreamResult 时调用。
func WithLogicalRequestLifetime(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	lifetime, finish := context.WithCancel(ctx)
	return context.WithValue(ctx, logicalRequestContextKey{}, lifetime.Done()), finish
}

// LogicalRequestDone 返回 conductor 提供的完成信号。直接 executor 调用没有
// 此信号时返回 nil，不能把任意 attempt context 的结束当成逻辑请求结束。
func LogicalRequestDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	done, _ := ctx.Value(logicalRequestContextKey{}).(<-chan struct{})
	return done
}
