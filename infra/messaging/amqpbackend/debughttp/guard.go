package debughttp

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

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
	if h == nil {
		panic("debughttp: Guard requires a non-nil handler")
	}
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
	if len(credentials) == 0 {
		panic("debughttp: BasicAuth requires at least one credential")
	}
	hashed := make([]basicCredential, 0, len(credentials))
	for user, pass := range credentials {
		if user == "" {
			panic("debughttp: BasicAuth username must not be empty")
		}
		if pass == "" {
			panic("debughttp: BasicAuth password must not be empty")
		}
		hashed = append(hashed, basicCredential{
			user: sha256.Sum256([]byte(user)),
			pass: sha256.Sum256([]byte(pass)),
		})
	}
	return func(r *http.Request) bool {
		user, pass, ok := r.BasicAuth()
		if !ok {
			return false
		}
		userHash := sha256.Sum256([]byte(user))
		passHash := sha256.Sum256([]byte(pass))
		// Walk every entry so timing doesn't reveal which user exists.
		match := 0
		for _, c := range hashed {
			userMatch := subtle.ConstantTimeCompare(userHash[:], c.user[:])
			passMatch := subtle.ConstantTimeCompare(passHash[:], c.pass[:])
			match |= userMatch & passMatch
		}
		return match == 1
	}
}

type basicCredential struct {
	user [sha256.Size]byte
	pass [sha256.Size]byte
}

// AllowFromHeader returns an Authenticator that approves the request when the
// named header equals expected (constant-time compare). Intended for service
// meshes that inject a signed identity header; do NOT trust client-supplied
// headers without an upstream verifier.
func AllowFromHeader(name, expected string) Authenticator {
	headerName := strings.TrimSpace(name)
	if !validHeaderName(headerName) {
		panic("debughttp: AllowFromHeader requires a valid non-empty header name")
	}
	if !validHeaderValue(expected) {
		panic("debughttp: AllowFromHeader requires a valid non-empty expected value")
	}
	// Hash both sides to a fixed 32 bytes before the constant-time compare
	// (mirrors BasicAuth above): comparing raw tokens of differing length
	// lets ConstantTimeCompare short-circuit on the length mismatch, leaking
	// the expected token's length via timing.
	want := sha256.Sum256([]byte(expected))
	return func(r *http.Request) bool {
		values := r.Header.Values(headerName)
		if len(values) != 1 {
			return false
		}
		if !validHeaderValue(values[0]) {
			return false
		}
		got := sha256.Sum256([]byte(values[0]))
		return subtle.ConstantTimeCompare(got[:], want[:]) == 1
	}
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, ",") || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}
