package contextutil

import "context"

// requestID is a named string type for request ID context uniqueness.
type requestID string

// ridKey is the context key for request IDs.
var ridKey Key[requestID]

// SetRequestID stores a request ID in the context.
func SetRequestID(ctx context.Context, id string) context.Context {
	return ridKey.Set(ctx, requestID(id))
}

// RequestID extracts the request ID from the context.
// Returns empty string if not set.
func RequestID(ctx context.Context) string {
	v, ok := ridKey.Get(ctx)
	if !ok {
		return ""
	}
	return string(v)
}
