package apikey

import (
	"fmt"
	"net/http"

	"github.com/bds421/rho-kit/authz/v2"
	"github.com/bds421/rho-kit/httpx/v2"
)

// RequireScopes returns middleware that allows a request only when the
// authenticated key carries every scope in required. It must be chained
// after [Middleware], which attaches the key's scopes to the context.
//
// Each required scope is validated against the shared authz registry at
// construction time: an unregistered scope panics at startup, turning a
// typo (which would otherwise silently reject every request) into an
// immediate, obvious failure.
//
// A request with no authenticated key yields 401; an authenticated key
// missing a required scope yields 403.
func RequireScopes(required ...authz.Scope) func(http.Handler) http.Handler {
	want := make([]string, len(required))
	for i, s := range required {
		if !authz.IsRegistered(s) {
			panic(fmt.Sprintf("middleware/apikey: RequireScopes given unregistered scope %q", s))
		}
		want[i] = string(s)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			granted, ok := ScopesFromContext(r)
			if !ok {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			have := make(map[string]struct{}, len(granted))
			for _, g := range granted {
				have[g] = struct{}{}
			}
			for _, need := range want {
				if _, ok := have[need]; !ok {
					httpx.WriteError(w, http.StatusForbidden, "insufficient scope")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
