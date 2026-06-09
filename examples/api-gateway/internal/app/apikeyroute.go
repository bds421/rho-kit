package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/bds421/rho-kit/authz/v2"
	apikeymw "github.com/bds421/rho-kit/httpx/v2/middleware/apikey"
	apikeycore "github.com/bds421/rho-kit/security/v2/apikey"
)

// scopeOrdersRead is registered once at package init so the demo's
// RequireScopes call passes the authz-registry validation. Real services
// register their scopes the same way, in one place per scope.
var scopeOrdersRead = authz.MustRegister("orders.read", "read a tenant's orders")

// newAPIKeyDemoHandler builds an API-key-protected route demonstrating the
// canonical issue → authenticate → scope-check flow using opaque keys
// (security/apikey + httpx/middleware/apikey).
//
// It returns the handler plus the one-time plaintext token for the key it
// issued at startup, so the smoke test (and a curious operator) can call the
// route. EXAMPLE ONLY: a real service issues keys on demand through the
// apikey.Manager behind an authenticated admin endpoint and NEVER logs the
// plaintext — the token is shown to the owner exactly once at issuance.
func newAPIKeyDemoHandler(ctx context.Context, logger *slog.Logger) (http.Handler, string, error) {
	repo := apikeycore.NewMemoryRepository()
	manager := apikeycore.NewManager(repo)

	_, token, err := manager.Issue(ctx, apikeycore.IssueOptions{
		Owner:  "demo-tenant",
		Scopes: []string{string(scopeOrdersRead)},
	})
	if err != nil {
		return nil, "", err
	}

	// Authenticate with the API key, then require the orders.read scope.
	core := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, _ := apikeymw.OwnerFromContext(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"owner":  owner,
			"orders": "orders for " + owner,
		})
	})
	handler := apikeymw.Middleware(apikeymw.Config{Repository: repo, Logger: logger})(
		apikeymw.RequireScopes(scopeOrdersRead)(core),
	)
	return handler, token.RevealString(), nil
}
