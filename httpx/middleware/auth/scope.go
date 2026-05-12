package auth

import (
	"net/http"
	"strings"

	"github.com/bds421/rho-kit/httpx/v2"
)

// RequireScope returns middleware that enforces API key scope authorization.
// It checks scopes from the request context (set by JWT verification in
// JWT/RequireS2SAuth).
//
// Fail-closed semantics:
//   - A request authenticated via the trusted-S2S mTLS branch bypasses the
//     check (verified internal caller).
//   - Otherwise the scopes string on context must contain the required
//     scope. An absent or empty scopes string is rejected.
//
// The previous "no scopes ⇒ pass through" rule was unsafe: it let any
// caller without a scopes claim — including a misconfigured route with no
// auth middleware in front — through to the handler. Routes that legitimately
// want to coexist with cookie-session callers must be split (one handler
// for scope-bearing API keys, one for sessions) instead of relying on this
// middleware to silently fall through.
func RequireScope(requiredScope string) func(http.Handler) http.Handler {
	if requiredScope == "" {
		panic("auth: RequireScope requires a non-empty scope name")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if IsTrustedS2S(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}
			scopes := scopesFromRequest(r)
			if scopes == "" || !hasScope(scopes, requiredScope) {
				httpx.WriteError(w, http.StatusForbidden, "insufficient scope")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireScopeStrict returns middleware that enforces API key scope authorization
// with fail-closed semantics. Unlike RequireScope, this rejects requests when
// scopes are absent — it does NOT fall through to cookie-session auth.
//
// Use this for machine-to-machine endpoints
// that must only be accessible via API keys with specific scopes, preventing
// privilege escalation via missing-header spoofing from adjacent containers.
func RequireScopeStrict(requiredScope string) func(http.Handler) http.Handler {
	if requiredScope == "" {
		panic("auth: RequireScopeStrict requires a non-empty scope name")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scopes := scopesFromRequest(r)
			if scopes == "" {
				httpx.WriteError(w, http.StatusForbidden, "scope header required")
				return
			}

			if !hasScope(scopes, requiredScope) {
				httpx.WriteError(w, http.StatusForbidden, "insufficient scope")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// scopesFromRequest reads scopes from context (set by JWT verification in JWT).
func scopesFromRequest(r *http.Request) string {
	return Scopes(r.Context())
}

// hasScope checks whether the comma-separated scopes string contains the given scope.
func hasScope(scopes, scope string) bool {
	for i, start := 0, 0; i <= len(scopes); i++ {
		if i == len(scopes) || scopes[i] == ',' {
			s := strings.TrimSpace(scopes[start:i])
			if s == scope {
				return true
			}
			start = i + 1
		}
	}
	return false
}
