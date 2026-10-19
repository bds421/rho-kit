package oidc

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
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

func TestMiddleware_BackChannelLogoutRouteIsOptIn(t *testing.T) {
	newHandler := func(opts ...Option) (http.Handler, *bool) {
		opts = append([]Option{
			WithSessionStore(kitoauth.NewMemorySessionStore()), WithStateStore(kitoauth.NewMemoryStateStore()), WithInMemoryStoresForTesting(),
		}, opts...)
		m := Module(kitoauth.Config{Issuer: testOIDCIssuer(t), ClientID: "web", RedirectURL: "https://app.example.test/oauth/callback"}, opts...).(*module)
		initModule(t, m)
		called := new(bool)
		return m.PublicMiddleware()[0].Func(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			*called = true
			w.WriteHeader(http.StatusTeapot)
		})), called
	}
	post := func(h http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/oauth/backchannel-logout", strings.NewReader("logout_token="))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}

	h, called := newHandler()
	assert.Equal(t, http.StatusTeapot, post(h).Code, "without the option the path belongs to the service")
	assert.True(t, *called)

	h, called = newHandler(WithBackChannelLogout())
	res := post(h)
	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.JSONEq(t, `{"error":"invalid_request"}`, res.Body.String())
	assert.False(t, *called)
}

func TestMiddleware_ExposesSessionAuthentication(t *testing.T) {
	sessions := kitoauth.NewMemorySessionStore()
	m := Module(kitoauth.Config{Issuer: testOIDCIssuer(t), ClientID: "web", RedirectURL: "https://app.example.test/oauth/callback"},
		WithSessionStore(sessions), WithStateStore(kitoauth.NewMemoryStateStore()), WithInMemoryStoresForTesting(),
	).(*module)
	initModule(t, m)
	authTime := time.Now().Add(-time.Minute).Truncate(time.Second)
	require.NoError(t, sessions.Put(context.Background(), "session-1", kitoauth.Session{
		SessionID: "session-1", UserID: "user-1", AMR: []string{"pwd", "otp"}, AuthTime: authTime,
	}, time.Minute))

	var seen bool
	h := m.PublicMiddleware()[0].Func(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		amr, at, ok := SessionAuthentication(r.Context())
		require.True(t, ok)
		assert.Equal(t, []string{"pwd", "otp"}, amr)
		assert.True(t, authTime.Equal(at))
		amr[0] = "mutated"
		again, _, _ := SessionAuthentication(r.Context())
		assert.Equal(t, "pwd", again[0], "callers get a copy")
		seen = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/orders", nil)
	req.AddCookie(&http.Cookie{Name: "kit_oauth_session", Value: "session-1"})
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.True(t, seen)

	_, _, ok := SessionAuthentication(context.Background())
	assert.False(t, ok)
}

func TestMiddleware_ExposesSessionAttributes(t *testing.T) {
	sessions := kitoauth.NewMemorySessionStore()
	m := Module(kitoauth.Config{Issuer: testOIDCIssuer(t), ClientID: "web", RedirectURL: "https://app.example.test/oauth/callback"},
		WithSessionStore(sessions), WithStateStore(kitoauth.NewMemoryStateStore()), WithInMemoryStoresForTesting(),
	).(*module)
	initModule(t, m)
	require.NoError(t, sessions.Put(context.Background(), "session-1", kitoauth.Session{
		SessionID: "session-1", UserID: "user-1", Attributes: map[string]string{"directory_user": "u-1"},
	}, time.Minute))

	var seen bool
	h := m.PublicMiddleware()[0].Func(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		attrs, ok := SessionAttributes(r.Context())
		require.True(t, ok)
		assert.Equal(t, "u-1", attrs["directory_user"])
		attrs["directory_user"] = "mutated"
		again, _ := SessionAttributes(r.Context())
		assert.Equal(t, "u-1", again["directory_user"], "callers get a copy")
		seen = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/orders", nil)
	req.AddCookie(&http.Cookie{Name: "kit_oauth_session", Value: "session-1"})
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.True(t, seen)

	_, ok := SessionAttributes(context.Background())
	assert.False(t, ok)
}
