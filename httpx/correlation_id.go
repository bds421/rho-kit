package httpx

import (
	"context"
	"net/http"
)

const correlationIDKey contextKey = "correlationID"

// correlationIDHeader is the canonical HTTP header name for correlation IDs.
const correlationIDHeader = "X-Correlation-Id"

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

// PropagateHTTP injects the correlation ID from context into an outbound HTTP request header.
// If no correlation ID is present in the context, this is a no-op.
func PropagateHTTP(ctx context.Context, req *http.Request) {
	if id := CorrelationID(ctx); id != "" {
		req.Header.Set(correlationIDHeader, id)
	}
}

// PropagateMessageHeader returns the correlation ID header key-value for messaging.
// Returns ("", "") if no correlation ID is present in the context.
func PropagateMessageHeader(ctx context.Context) (key, value string) {
	if id := CorrelationID(ctx); id != "" {
		return correlationIDHeader, id
	}
	return "", ""
}
