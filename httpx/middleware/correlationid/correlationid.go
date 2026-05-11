// Package correlationid provides HTTP middleware for propagating correlation IDs
// across service boundaries. A correlation ID groups related requests that belong
// to the same logical operation, unlike a request ID which is unique per request.
//
// asvs: V7.1.1
package correlationid

import (
	"net/http"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/httpx/v2/middleware/internal/idutil"
)

// Header is the canonical HTTP header name for correlation IDs.
const Header = "X-Correlation-Id"

// maxCorrelationIDLen is the maximum length for an incoming correlation ID header.
const maxCorrelationIDLen = contextutil.MaxCorrelationIDLen

// WithCorrelationID reads the correlation ID from the X-Correlation-Id header.
// If absent or invalid, it generates a new one. The ID is stored in the request
// context and set on the response header.
func WithCorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := singletonHeaderValue(r.Header, Header)
		if !isValidCorrelationID(id) {
			id = contextutil.NewID()
		}
		w.Header().Set(Header, id)
		ctx := contextutil.SetCorrelationID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isValidCorrelationID returns true if id is a safe request/correlation token.
func isValidCorrelationID(id string) bool {
	return idutil.IsValid(id, maxCorrelationIDLen)
}

func singletonHeaderValue(h http.Header, name string) string {
	values := h.Values(name)
	if len(values) != 1 {
		return ""
	}
	return values[0]
}
