package auth

import (
	"net/http"
	"strings"

	"github.com/bds421/rho-kit/httpx"
)

// RequireScope returns middleware that enforces API key scope authorization.
// It checks scopes from the request context (set by JWT verification in
// RequireUserWithJWT/RequireS2SAuth).
//
// When scopes are absent entirely (cookie-session auth), the request passes
// through — session-based users are governed by RBAC, not scopes.
//
// For machine-to-machine endpoints that must NEVER allow cookie-session fallback,
// use RequireScopeStrict instead.
func RequireScope(requiredScope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scopes := scopesFromRequest(r)
			if scopes == "" {
				// No scopes = cookie-session auth; RBAC applies instead.
				next.ServeHTTP(w, r)
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

// RequireScopeStrict returns middleware that enforces API key scope authorization
// with fail-closed semantics. Unlike RequireScope, this rejects requests when
// scopes are absent — it does NOT fall through to cookie-session auth.
//
// Use this for machine-to-machine endpoints
// that must only be accessible via API keys with specific scopes, preventing
// privilege escalation via missing-header spoofing from adjacent containers.
func RequireScopeStrict(requiredScope string) func(http.Handler) http.Handler {
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

// scopesFromRequest reads scopes from context (set by JWT verification in RequireUserWithJWT).
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
