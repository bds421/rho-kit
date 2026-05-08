package debughttp

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/bds421/rho-kit/core/v2/config"
)

// Authenticator is invoked on each debug request. Return true to allow the
// request to proceed. A common implementation is BasicAuth below; production
// deployments should prefer mTLS or a signed-token check.
type Authenticator func(r *http.Request) bool

// Guard wraps a debug handler with two safety checks that MUST both pass for
// the handler to run:
//
//  1. The environment string must equal the literal "development" (per
//     [config.IsDevelopment]). Any other value — "dev", "staging", "test",
//     "local", "" — is treated as non-dev and rejected. The strict
//     equality is intentional: the kit ships fail-closed on this gate so
//     a typo'd env value cannot accidentally expose a debug surface in
//     production.
//  2. The Authenticator must approve the request.
//
// Either failure returns 404 Not Found (intentionally indistinguishable from
// "endpoint disabled" so production probes cannot fingerprint the debug
// surface).
//
// debughttp endpoints publish or invoke handlers based on attacker-supplied
// JSON; they MUST be guarded behind both gates before being mounted on any
// listener that is reachable from outside the operator's debug environment.
func Guard(environment string, auth Authenticator, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !config.IsDevelopment(environment) {
			http.NotFound(w, r)
			return
		}
		if auth == nil || !auth(r) {
			// Send WWW-Authenticate so curl/dev tools get a usable prompt
			// without revealing that the endpoint exists in production.
			w.Header().Set("WWW-Authenticate", `Basic realm="rho-kit debug"`)
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// BasicAuth returns an Authenticator that requires the request to carry HTTP
// Basic credentials matching one of the supplied user→password pairs.
// Comparison is constant-time to avoid leaking valid usernames via timing.
//
// Use only over TLS — Basic auth credentials are sent in plaintext over the
// wire on every request.
func BasicAuth(credentials map[string]string) Authenticator {
	// Pre-hash to constant length is overkill for this use; we compare each
	// candidate with subtle.ConstantTimeCompare and OR the results to make
	// timing independent of which (if any) entry matched.
	return func(r *http.Request) bool {
		user, pass, ok := r.BasicAuth()
		if !ok {
			return false
		}
		// Walk every entry so timing doesn't reveal which user exists.
		match := 0
		for u, p := range credentials {
			userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(u))
			passMatch := subtle.ConstantTimeCompare([]byte(pass), []byte(p))
			match |= userMatch & passMatch
		}
		return match == 1
	}
}

// AllowFromHeader returns an Authenticator that approves the request when the
// named header equals expected (constant-time compare). Intended for service
// meshes that inject a signed identity header; do NOT trust client-supplied
// headers without an upstream verifier.
func AllowFromHeader(name, expected string) Authenticator {
	headerName := strings.TrimSpace(name)
	want := []byte(expected)
	return func(r *http.Request) bool {
		got := []byte(r.Header.Get(headerName))
		if len(got) == 0 {
			return false
		}
		return subtle.ConstantTimeCompare(got, want) == 1
	}
}
