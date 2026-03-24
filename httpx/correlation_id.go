package httpx

import (
	"context"
	"net/http"

	"github.com/bds421/rho-kit/core/contextutil"
)

// correlationIDHeader is the canonical HTTP header name for correlation IDs.
const correlationIDHeader = "X-Correlation-Id"

// SetCorrelationID stores a correlation ID in the context.
//
// Deprecated: Use contextutil.SetCorrelationID instead.
func SetCorrelationID(ctx context.Context, id string) context.Context {
	return contextutil.SetCorrelationID(ctx, id)
}

// CorrelationID retrieves the correlation ID from context.
// Returns empty string if not set.
//
// Deprecated: Use contextutil.CorrelationID instead.
func CorrelationID(ctx context.Context) string {
	return contextutil.CorrelationID(ctx)
}

// PropagateHTTP injects the correlation ID from context into an outbound HTTP request header.
// If no correlation ID is present in the context, this is a no-op.
func PropagateHTTP(ctx context.Context, req *http.Request) {
	if id := contextutil.CorrelationID(ctx); id != "" {
		req.Header.Set(correlationIDHeader, id)
	}
}

// PropagateMessageHeader returns the correlation ID header key-value for messaging.
// Returns ("", "") if no correlation ID is present in the context.
func PropagateMessageHeader(ctx context.Context) (key, value string) {
	if id := contextutil.CorrelationID(ctx); id != "" {
		return correlationIDHeader, id
	}
	return "", ""
}
