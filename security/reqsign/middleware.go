package reqsign

import (
	"bytes"
	"io"
	"net/http"

	"github.com/bds421/rho-kit/httpx"
)

// maxBodySize is the maximum request body size the middleware will buffer
// for signature verification (1 MiB).
const maxBodySize = 1 << 20

// RequireSignedRequest returns middleware that verifies request signatures.
// Requests with missing or invalid signatures receive a 401 Unauthorized response.
// The request body is read, verified, and then replaced so downstream handlers
// can still read it.
func RequireSignedRequest(store KeyStore, opts ...VerifyOption) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body []byte

			if r.Body != nil && r.Body != http.NoBody {
				limited := io.LimitReader(r.Body, maxBodySize+1)
				var err error
				body, err = io.ReadAll(limited)
				if err != nil {
					httpx.WriteError(w, http.StatusBadRequest, "failed to read request body")
					return
				}
				if int64(len(body)) > maxBodySize {
					httpx.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
					return
				}
				if err := r.Body.Close(); err != nil {
					httpx.WriteError(w, http.StatusInternalServerError, "internal error")
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
			}

			if err := VerifyRequest(r, body, store, opts...); err != nil {
				httpx.Logger(r.Context(), nil).Debug("request signature verification failed", "error", err)
				httpx.WriteError(w, http.StatusUnauthorized, "invalid or missing signature")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
