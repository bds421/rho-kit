package requestid

import (
	"net/http"

	"github.com/bds421/rho-kit/httpx"
	"github.com/bds421/rho-kit/httpx/middleware/internal/idutil"
)

// Header is the canonical HTTP header name for request IDs.
const Header = "X-Request-Id"

// maxRequestIDLen is the maximum length for an incoming X-Request-Id header.
const maxRequestIDLen = 128

// WithRequestID ensures every request has a unique identifier.
// It uses the incoming X-Request-Id header if present and valid, otherwise
// generates one. The ID is set on the response header and stored in the context.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(Header)
		if !isValidRequestID(id) {
			id = idutil.Generate()
		}
		w.Header().Set(Header, id)
		ctx := httpx.SetRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isValidRequestID returns true if id is non-empty, within length limits,
// and contains only printable ASCII characters (excluding space).
func isValidRequestID(id string) bool {
	return idutil.IsValid(id, maxRequestIDLen)
}
