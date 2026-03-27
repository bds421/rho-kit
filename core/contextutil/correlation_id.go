package contextutil

import "context"

// correlationID is a named string type for correlation ID context uniqueness.
type correlationID string

// cidKey is the context key for correlation IDs.
var cidKey Key[correlationID]

// SetCorrelationID stores a correlation ID in the context.
func SetCorrelationID(ctx context.Context, id string) context.Context {
	return cidKey.Set(ctx, correlationID(id))
}

// CorrelationID extracts the correlation ID from the context.
// Returns empty string if not set.
//
// To propagate the correlation ID to an outbound messaging.Message, use:
//
//	msg = msg.WithHeader("X-Correlation-Id", contextutil.CorrelationID(ctx))
//
// For outbound HTTP requests, use httpx.PropagateHTTP instead.
func CorrelationID(ctx context.Context) string {
	v, ok := cidKey.Get(ctx)
	if !ok {
		return ""
	}
	return string(v)
}
