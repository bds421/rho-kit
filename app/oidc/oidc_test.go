package oidc

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	kitoauth "github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/security/v2/identity"
)

type testHTTPClientModule struct{ client *http.Client }

func (m testHTTPClientModule) Name() string                                  { return app.HTTPClientModuleName }
func (m testHTTPClientModule) Init(context.Context, app.ModuleContext) error { return nil }
func (m testHTTPClientModule) Populate(*app.Infrastructure)                  {}
func (m testHTTPClientModule) Stop(context.Context) error                    { return nil }
func (m testHTTPClientModule) HealthChecks() []health.DependencyCheck        { return nil }
func (m testHTTPClientModule) Client() *http.Client                          { return m.client }

// testOIDCIssuer serves only discovery: route/session middleware tests don't
// execute a code exchange, but construction must still prove OIDC discovery.
func testOIDCIssuer(t *testing.T) string {
	t.Helper()
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/authorize",
			"token_endpoint":         issuer + "/token",
			"jwks_uri":               issuer + "/jwks",
		})
	}))
	issuer = srv.URL
	t.Cleanup(srv.Close)
	return issuer
}

func initModule(t *testing.T, m *module) {
	t.Helper()
	mc, err := app.TestModuleContext(testHTTPClientModule{client: http.DefaultClient}, m)
	require.NoError(t, err)
	mc.Logger = slog.Default()
	require.NoError(t, m.Init(context.Background(), mc))
}

func TestModule_RequiresDurableStoreChoices(t *testing.T) {
	assert.PanicsWithValue(t, "app/oidc: WithSessionStore is required", func() {
		Module(kitoauth.Config{})
	})
	assert.PanicsWithValue(t, "app/oidc: WithStateStore is required", func() {
		Module(kitoauth.Config{}, WithSessionStore(kitoauth.NewMemorySessionStore()))
	})
	assert.PanicsWithValue(t, "app/oidc: memory session/state stores require WithInMemoryStoresForTesting; use auth/oauth2/redis in production", func() {
		Module(kitoauth.Config{}, WithSessionStore(kitoauth.NewMemorySessionStore()), WithStateStore(kitoauth.NewMemoryStateStore()))
	})
}

func TestMiddleware_ProjectsBrowserSessionPrincipal(t *testing.T) {
	sessions := kitoauth.NewMemorySessionStore()
	states := kitoauth.NewMemoryStateStore()
	m := Module(kitoauth.Config{Issuer: testOIDCIssuer(t), ClientID: "web", RedirectURL: "https://app.example.test/oauth/callback"},
		WithSessionStore(sessions),
		WithStateStore(states),
		WithInMemoryStoresForTesting(),
		WithPrincipalProfile(identity.MappingProfile{TenantClaim: "org", ScopesClaim: "scope"}),
	).(*module)
	initModule(t, m)
	require.NoError(t, sessions.Put(context.Background(), "session-1", kitoauth.Session{
		SessionID: "session-1", UserID: "auth0|user-1", Claims: map[string]any{"org": "org-1", "scope": "orders:read"},
	}, time.Minute))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := identity.FromContext(r.Context())
		if !ok {
			t.Fatal("expected OIDC principal")
		}
		assert.Equal(t, "auth0|user-1", p.Subject)
		assert.Equal(t, "org-1", p.Tenant)
		assert.Equal(t, []string{"orders:read"}, p.Scopes)
		w.WriteHeader(http.StatusNoContent)
	})
	h := m.PublicMiddleware()[0].Func(next)
	req := httptest.NewRequest(http.MethodGet, "/orders", nil)
	req.AddCookie(&http.Cookie{Name: "kit_oauth_session", Value: "session-1"})
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	assert.Equal(t, http.StatusNoContent, res.Code)
}

func TestMiddleware_LeavesUnauthenticatedRoutesToService(t *testing.T) {
	m := Module(kitoauth.Config{Issuer: testOIDCIssuer(t), ClientID: "web", RedirectURL: "https://app.example.test/oauth/callback"},
		WithSessionStore(kitoauth.NewMemorySessionStore()), WithStateStore(kitoauth.NewMemoryStateStore()), WithInMemoryStoresForTesting(),
	).(*module)
	initModule(t, m)
	called := false
	h := m.PublicMiddleware()[0].Func(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.True(t, called)
}
