package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/crypto/v2/passhash"
	"github.com/bds421/rho-kit/httpx/v2/middleware/auth"
	"github.com/bds421/rho-kit/security/v2/apikey"
	"github.com/bds421/rho-kit/security/v2/session"
)

func TestOAuthAccessBearerAuthenticator_MapsIdentity(t *testing.T) {
	const prefix = "rhoac"
	h := auth.ChainMiddleware(
		auth.NewOAuthAccessBearerAuthenticator(
			auth.OAuthAccessVerifierFunc(func(_ context.Context, token string) (auth.Identity, error) {
				assert.Equal(t, prefix+"_lookup_secret", token)
				return auth.Identity{
					Subject:   testUserID,
					Actor:     "client99",
					ActorKind: auth.ActorOAuthClient,
					Tenant:    "tenant-a",
					ScopeList: []string{"read"},
				}, nil
			}),
			prefix,
		),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, testUserID, auth.Subject(r.Context()))
		assert.Equal(t, "client99", auth.Actor(r.Context()))
		assert.Equal(t, auth.ActorOAuthClient, auth.ActorKindFromContext(r.Context()))
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+prefix+"_lookup_secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestOAuthAccessSessionAuthenticator_ReturnsMachineIdentity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer oauthpayload.oauthmac")
	id, err := auth.NewOAuthAccessSessionAuthenticator(
		auth.OAuthAccessVerifierFunc(func(_ context.Context, token string) (auth.Identity, error) {
			require.Equal(t, "oauthpayload.oauthmac", token)
			return auth.Identity{
				Subject: testUserID, Actor: "client1", ActorKind: auth.ActorOAuthClient,
				Tenant: "tenant-a",
			}, nil
		}),
	).Authenticate(req)
	require.NoError(t, err)
	assert.Equal(t, "client1", id.Actor)
	assert.Equal(t, auth.ActorOAuthClient, id.ActorKind)
}

func TestOAuthAccessSessionAuthenticator_BeforeSession(t *testing.T) {
	root := []byte("0123456789abcdef0123456789abcdef")
	signer, err := session.NewSigner(root, "session")
	require.NoError(t, err)

	sessionToken, err := signer.Mint(session.Claims{
		UserID: testUserID, Tenant: "tenant-a", Role: "member",
		TokenVersion: 1, Exp: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)

	var oauthCalled bool
	oauthAuth := auth.NewOAuthAccessSessionAuthenticator(
		auth.OAuthAccessVerifierFunc(func(_ context.Context, token string) (auth.Identity, error) {
			if token != "oauthpayload.oauthmac" {
				return auth.Identity{}, auth.ErrUnauthenticated
			}
			oauthCalled = true
			return auth.Identity{
				Subject: testUserID, Actor: "client1", ActorKind: auth.ActorOAuthClient,
				Tenant: "tenant-a",
			}, nil
		}),
	)

	t.Run("oauth token", func(t *testing.T) {
		h := auth.ChainMiddleware(oauthAuth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "client1", auth.Actor(r.Context()))
			require.Equal(t, auth.ActorOAuthClient, auth.ActorKindFromContext(r.Context()))
			w.WriteHeader(http.StatusNoContent)
		}))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer oauthpayload.oauthmac")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.True(t, oauthCalled)
	})

	t.Run("session token after oauth skip", func(t *testing.T) {
		h := auth.ChainMiddleware(
			oauthAuth,
			auth.NewSessionAuthenticator(session.Validator{Signer: signer}),
		)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, testUserID, auth.Actor(r.Context()))
			require.Equal(t, auth.ActorUser, auth.ActorKindFromContext(r.Context()))
			w.WriteHeader(http.StatusNoContent)
		}))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+sessionToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNoContent, rec.Code)
	})
}

func TestChainMiddleware_ScopedKeyRequirePermission(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	repo := apikey.NewMemoryPrefixRepository()
	key, token, err := apikey.GenerateScoped(apikey.ScopedGenerateOptions{
		Tenant: "tenant-a", SubjectUserID: testUserID, Role: "member",
		Scopes: []string{"billing:read"},
		Now: now, HashParams: passhash.Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32},
	})
	require.NoError(t, err)
	require.NoError(t, repo.InsertScoped(context.Background(), key))

	scopedResolver := apikey.NewScopedResolver(repo, apikey.ScopedTokenPrefixAPI, apikey.WithScopedClock(func() time.Time { return now }))
	h := auth.ChainMiddleware(
		auth.NewScopedKeyBearerAuthenticator(scopedResolver),
	)(auth.RequirePermission("billing:read")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token.RevealString())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}