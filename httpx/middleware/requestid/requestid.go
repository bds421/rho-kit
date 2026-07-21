package requestid

import (
	"net/http"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/httpx/v2/middleware/internal/idutil"
)

// Header is the canonical HTTP header name for request IDs.
const Header = "X-Request-Id"

// maxRequestIDLen is the maximum length for an incoming X-Request-Id header.
const maxRequestIDLen = contextutil.MaxCorrelationIDLen

// WithRequestID ensures every request has a unique identifier.
// It uses the incoming X-Request-Id header if present and valid, otherwise
// generates one. The ID is set on the response header and stored in the context.
//
// Security note: the inbound header is fully caller-controlled for any
// unauthenticated client. The token alphabet check prevents CR/LF injection,
// but an attacker can still choose an ID that collides with a legitimate
// request to pollute log/SIEM correlation. Strip or re-stamp X-Request-Id
// at the ingress / trusted proxy if forensic integrity of the ID matters;
// this middleware does not implement a trusted-proxy gate.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := singletonHeaderValue(r.Header, Header)
		if !isValidRequestID(id) {
			id = contextutil.GenerateID()
		}
		w.Header().Set(Header, id)
		ctx := contextutil.SetRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isValidRequestID returns true if id is a safe request/correlation token.
func isValidRequestID(id string) bool {
	return idutil.IsValid(id, maxRequestIDLen)
}

func singletonHeaderValue(h http.Header, name string) string {
	values := h.Values(name)
	if len(values) != 1 {
		return ""
	}
	return values[0]
}
