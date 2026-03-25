package httpx

import (
	"context"
	"net/http"

	"github.com/bds421/rho-kit/core/contextutil"
)

// correlationIDHeaderName is the HTTP header used to propagate correlation IDs.
// Must match correlationid.Header.
const correlationIDHeaderName = "X-Correlation-Id"

// PropagateHTTP injects the correlation ID from context into an outbound HTTP request header.
// If no correlation ID is present in the context, this is a no-op.
func PropagateHTTP(ctx context.Context, req *http.Request) {
	if id := contextutil.CorrelationID(ctx); id != "" {
		req.Header.Set(correlationIDHeaderName, id)
	}
}

// PropagateMessageHeader returns the correlation ID header key-value for messaging.
// Returns ("", "") if no correlation ID is present in the context.
func PropagateMessageHeader(ctx context.Context) (key, value string) {
	if id := contextutil.CorrelationID(ctx); id != "" {
		return correlationIDHeaderName, id
	}
	return "", ""
}
