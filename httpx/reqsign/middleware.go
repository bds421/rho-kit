package reqsign

import (
	"bytes"
	"io"
	"net/http"

	"github.com/bds421/rho-kit/crypto/v2/signing"
	"github.com/bds421/rho-kit/httpx/v2"
)

// RequireSignedRequest returns middleware that verifies request signatures.
// Requests with missing or invalid signatures receive a 401 Unauthorized response.
// The request body is read, verified, and then replaced so downstream handlers
// can still read it.
//
// FR-025 [HIGH]: a non-nil NonceStore is REQUIRED. Without it, a
// captured signed request can be replayed up to maxAge later. Pass the
// store via [WithNonceStore]; the constructor panics on nil so wiring
// errors are caught at startup, not after exposure to the network.
func RequireSignedRequest(store signing.KeyStore, opts ...VerifyOption) func(http.Handler) http.Handler {
	if store == nil {
		panic(nilKeyStoreMsg)
	}

	// Pre-apply all options once at construction time to avoid re-applying
	// them on every request.
	cfg := verifyConfig{
		signer:      defaultSigner,
		maxAge:      signing.DefaultSignatureMaxAge,
		maxBodySize: MaxBodySize,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.nonceStore == nil {
		panic(nilNonceStoreMsg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body []byte

			if r.Body != nil && r.Body != http.NoBody {
				// Close the original body; it is replaced with a bytes.NewReader below.
				defer func() { _ = r.Body.Close() }()
				var err error
				body, err = io.ReadAll(io.LimitReader(r.Body, cfg.maxBodySize+1))
				if err != nil {
					httpx.Logger(r.Context(), nil).Debug("failed to read request body", "error", err)
					httpx.WriteError(w, http.StatusBadRequest, "failed to read request body")
					return
				}
				if int64(len(body)) > cfg.maxBodySize {
					httpx.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
			}

			if err := verifyRequestWithConfig(r, body, store, cfg); err != nil {
				httpx.Logger(r.Context(), nil).Debug("request signature verification failed", "error", err)
				httpx.WriteError(w, http.StatusUnauthorized, "invalid or missing signature")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
