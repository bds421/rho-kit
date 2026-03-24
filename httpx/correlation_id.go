package httpx

import "context"

const correlationIDKey contextKey = "correlationID"

// SetCorrelationID stores a correlation ID in the context.
// Used by the WithCorrelationID middleware to propagate IDs across service boundaries.
func SetCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}

// CorrelationID retrieves the correlation ID from context.
// Returns empty string if not set.
func CorrelationID(ctx context.Context) string {
	v, _ := ctx.Value(correlationIDKey).(string)
	return v
}
