// Package correlationid provides HTTP middleware for propagating correlation IDs
// across service boundaries. A correlation ID groups related requests that belong
// to the same logical operation, unlike a request ID which is unique per request.
package correlationid

import (
	"context"
	"net/http"

	"github.com/bds421/rho-kit/httpx"
	"github.com/bds421/rho-kit/httpx/middleware/internal/idutil"
)

// Header is the canonical HTTP header name for correlation IDs.
const Header = "X-Correlation-Id"

// maxCorrelationIDLen is the maximum length for an incoming correlation ID header.
const maxCorrelationIDLen = 128

// WithCorrelationID reads the correlation ID from the X-Correlation-Id header.
// If absent or invalid, it generates a new one. The ID is stored in the request
// context and set on the response header.
func WithCorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(Header)
		if !isValidCorrelationID(id) {
			id = idutil.Generate()
		}
		w.Header().Set(Header, id)
		ctx := httpx.SetCorrelationID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// PropagateHTTP injects the correlation ID from context into an outbound HTTP request.
// If no correlation ID is present in the context, this is a no-op.
func PropagateHTTP(ctx context.Context, req *http.Request) {
	if id := httpx.CorrelationID(ctx); id != "" {
		req.Header.Set(Header, id)
	}
}

// PropagateMessageHeader returns the correlation ID header key-value for messaging.
// Returns ("", "") if no correlation ID is present in the context.
func PropagateMessageHeader(ctx context.Context) (key, value string) {
	if id := httpx.CorrelationID(ctx); id != "" {
		return Header, id
	}
	return "", ""
}

// isValidCorrelationID returns true if id is non-empty, within length limits,
// and contains only printable ASCII characters.
func isValidCorrelationID(id string) bool {
	return idutil.IsValid(id, maxCorrelationIDLen)
}
