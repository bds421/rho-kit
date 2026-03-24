package httpx

import "context"

type contextKey string

const requestIDKey contextKey = "requestID"

// SetRequestID stores a request ID in the context.
// Used by the WithRequestID middleware to propagate IDs through the handler chain.
func SetRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID extracts the request ID from the context.
func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey).(string)
	return v
}
