package oidc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kitoauth "github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/core/v2/secret"
	"github.com/bds421/rho-kit/security/v2/identity"
)

const rolesClaim = "urn:zitadel:iam:org:project:roles"

func newTestModule(t *testing.T, sessions *kitoauth.MemorySessionStore, opts ...Option) *module {
	t.Helper()
	opts = append([]Option{
		WithSessionStore(sessions), WithStateStore(kitoauth.NewMemoryStateStore()), WithInMemoryStoresForTesting(),
	}, opts...)
	m := Module(kitoauth.Config{Issuer: testOIDCIssuer(t), ClientID: "web", RedirectURL: "https://app.example.test/oauth/callback"}, opts...).(*module)
	initModule(t, m)
	return m
}

func serveWithSession(m *module, next http.HandlerFunc) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/orders", nil)
	req.AddCookie(&http.Cookie{Name: "kit_oauth_session", Value: "session-1"})
	res := httptest.NewRecorder()
	m.PublicMiddleware()[0].Func(next).ServeHTTP(res, req)
	return res
}

func TestSessionClaims_ExposeObjectClaims(t *testing.T) {
	sessions := kitoauth.NewMemorySessionStore()
	m := newTestModule(t, sessions)
	require.NoError(t, sessions.Put(context.Background(), "session-1", kitoauth.Session{
		SessionID: "session-1", UserID: "user-1",
		Claims: map[string]any{
			rolesClaim: map[string]any{"analyst": map[string]any{"org-1": "example.test"}},
			"groups":   []any{"a", "b"},
		},
	}, time.Minute))

	res := serveWithSession(m, func(w http.ResponseWriter, r *http.Request) {
		claims, ok := SessionClaims(r.Context())
		require.True(t, ok)
		roles, ok := claims[rolesClaim].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, roles, "analyst")

		// Mutating the returned copy must not leak into later reads.
		roles["admin"] = map[string]any{}
		claims["groups"].([]any)[0] = "mutated"
		again, _ := SessionClaims(r.Context())
		assert.NotContains(t, again[rolesClaim].(map[string]any), "admin")
		assert.Equal(t, "a", again["groups"].([]any)[0])

		var typed struct {
			Roles  map[string]map[string]string `json:"urn:zitadel:iam:org:project:roles"`
			Groups []string                     `json:"groups"`
		}
		require.NoError(t, DecodeSessionClaims(r.Context(), &typed))
		assert.Equal(t, map[string]map[string]string{"analyst": {"org-1": "example.test"}}, typed.Roles)
		assert.Equal(t, []string{"a", "b"}, typed.Groups)
		w.WriteHeader(http.StatusNoContent)
	})
	assert.Equal(t, http.StatusNoContent, res.Code)
}

func TestSessionClaims_AbsentWithoutSession(t *testing.T) {
	_, ok := SessionClaims(context.Background())
	assert.False(t, ok)
	var dst map[string]any
	assert.ErrorIs(t, DecodeSessionClaims(context.Background(), &dst), ErrNoSessionClaims)
}

func TestMiddleware_OptionalMappedClaims(t *testing.T) {
	profile := identity.MappingProfile{TenantClaim: "org"}
	sessions := kitoauth.NewMemorySessionStore()
	require.NoError(t, sessions.Put(context.Background(), "session-1", kitoauth.Session{SessionID: "session-1", UserID: "user-1"}, time.Minute))

	strict := newTestModule(t, sessions, WithPrincipalProfile(profile))
	res := serveWithSession(strict, func(http.ResponseWriter, *http.Request) { t.Fatal("must not reach handler") })
	assert.Equal(t, http.StatusUnauthorized, res.Code, "default: missing mapped claim rejects")

	profile.AllowMissingClaims = true
	lenient := newTestModule(t, sessions, WithPrincipalProfile(profile))
	res = serveWithSession(lenient, func(w http.ResponseWriter, r *http.Request) {
		p, ok := identity.FromContext(r.Context())
		require.True(t, ok)
		assert.Equal(t, "user-1", p.Subject)
		assert.Empty(t, p.Tenant)
		w.WriteHeader(http.StatusNoContent)
	})
	assert.Equal(t, http.StatusNoContent, res.Code)
}

func TestMiddleware_RejectedRefreshTreatsSessionAsExpired(t *testing.T) {
	sessions := kitoauth.NewMemorySessionStore()
	m := newTestModule(t, sessions, WithClientOption(kitoauth.WithTokenRefresh(time.Minute)))
	require.NoError(t, sessions.Put(context.Background(), "session-1", kitoauth.Session{
		SessionID: "session-1", UserID: "user-1",
		AccessToken: secret.NewFromString("a"), RefreshToken: secret.NewFromString("r"),
		Expiry: time.Now().Add(-time.Minute),
	}, time.Minute))
	res := serveWithSession(m, func(w http.ResponseWriter, r *http.Request) {
		_, ok := identity.FromContext(r.Context())
		assert.False(t, ok, "expired session must not project a principal")
		_, ok = SessionClaims(r.Context())
		assert.False(t, ok)
		w.WriteHeader(http.StatusNoContent)
	})
	assert.Equal(t, http.StatusNoContent, res.Code)
}
