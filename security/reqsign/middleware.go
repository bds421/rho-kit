package reqsign

import (
	"bytes"
	"io"
	"net/http"

	"github.com/bds421/rho-kit/httpx"
)


// RequireSignedRequest returns middleware that verifies request signatures.
// Requests with missing or invalid signatures receive a 401 Unauthorized response.
// The request body is read, verified, and then replaced so downstream handlers
// can still read it.
func RequireSignedRequest(store KeyStore, opts ...VerifyOption) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body []byte

			if r.Body != nil && r.Body != http.NoBody {
				defer r.Body.Close()
				var err error
				body, err = io.ReadAll(io.LimitReader(r.Body, MaxBodySize+1))
				if err != nil {
					httpx.Logger(r.Context(), nil).Debug("failed to read request body", "error", err)
					httpx.WriteError(w, http.StatusBadRequest, "failed to read request body")
					return
				}
				if int64(len(body)) > MaxBodySize {
					httpx.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
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
