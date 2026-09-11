package httpx

import "context"

// contextWithRequestID 把请求 ID 存入 context。
func contextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// requestIDFromContext 从 context 取回请求 ID，缺失时返回空串。
func requestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}
