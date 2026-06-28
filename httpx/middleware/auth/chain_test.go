package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/crypto/v2/passhash"
	"github.com/bds421/rho-kit/httpx/v2/middleware/auth"
	"github.com/bds421/rho-kit/security/v2/apikey"
	"github.com/bds421/rho-kit/security/v2/session"
)

func TestChainMiddleware_FallsThroughToScopedKey(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	root := []byte("0123456789abcdef0123456789abcdef")
	signer, err := session.NewSigner(root, "session")
	require.NoError(t, err)

	repo := apikey.NewMemoryPrefixRepository()
	key, token, err := apikey.GenerateScoped(apikey.ScopedGenerateOptions{
		Tenant: "tenant-a", SubjectUserID: testUserID, Role: "member",
		Now: now, HashParams: passhash.Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32},
	})
	require.NoError(t, err)
	require.NoError(t, repo.InsertScoped(context.Background(), key))

	sessionValidator := session.Validator{Signer: signer}
	scopedResolver := apikey.NewScopedResolver(repo, apikey.ScopedTokenPrefixAPI, apikey.WithScopedClock(func() time.Time { return now }))

	var gotTenant, gotRole string
	h := auth.ChainMiddleware(
		auth.NewSessionAuthenticator(sessionValidator),
		auth.NewScopedKeyBearerAuthenticator(scopedResolver),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = auth.Tenant(r.Context())
		gotRole = auth.Role(r.Context())
		assert.Equal(t, testUserID, auth.Subject(r.Context()))
		assert.Equal(t, testUserID, auth.UserID(r.Context()))
		assert.Equal(t, auth.ActorAPIKey, auth.ActorKindFromContext(r.Context()))
		assert.NotEmpty(t, auth.Actor(r.Context()))
		assert.True(t, auth.IsMachine(r.Context()))
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token.RevealString())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "tenant-a", gotTenant)
	assert.Equal(t, "member", gotRole)
}

func TestChainMiddleware_FallsThroughNonSessionBearer(t *testing.T) {
	root := []byte("0123456789abcdef0123456789abcdef")
	signer, err := session.NewSigner(root, "session")
	require.NoError(t, err)

	var jwtStrategyCalled atomic.Bool
	h := auth.ChainMiddleware(
		auth.NewSessionAuthenticator(session.Validator{Signer: signer}),
		auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
			jwtStrategyCalled.Store(true)
			return auth.Identity{UserID: testUserID}, nil
		}),
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer aaa.bbb.ccc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.True(t, jwtStrategyCalled.Load(), "non-session Bearer tokens must fall through to later strategies")
}

func TestChainMiddleware_ScopedKeyPopulatesScopes(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	repo := apikey.NewMemoryPrefixRepository()
	key, token, err := apikey.GenerateScoped(apikey.ScopedGenerateOptions{
		Tenant: "tenant-a", SubjectUserID: testUserID, Role: "member",
		Scopes: []string{"read:contacts"},
		Now:    now, HashParams: passhash.Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32},
	})
	require.NoError(t, err)
	require.NoError(t, repo.InsertScoped(context.Background(), key))

	scopedResolver := apikey.NewScopedResolver(repo, apikey.ScopedTokenPrefixAPI, apikey.WithScopedClock(func() time.Time { return now }))
	h := auth.ChainMiddleware(
		auth.NewScopedKeyBearerAuthenticator(scopedResolver),
	)(auth.RequireScope("read:contacts")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "read:contacts", auth.Scopes(r.Context()))
		w.WriteHeader(http.StatusNoContent)
	})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token.RevealString())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestChainMiddleware_UnboundScopedKey(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	repo := apikey.NewMemoryPrefixRepository()
	key, token, err := apikey.GenerateScoped(apikey.ScopedGenerateOptions{
		Tenant: "tenant-a", Role: "admin",
		Now: now, HashParams: passhash.Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32},
	})
	require.NoError(t, err)
	require.NoError(t, repo.InsertScoped(context.Background(), key))

	scopedResolver := apikey.NewScopedResolver(repo, apikey.ScopedTokenPrefixAPI, apikey.WithScopedClock(func() time.Time { return now }))
	h := auth.ChainMiddleware(
		auth.NewScopedKeyBearerAuthenticator(scopedResolver),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, auth.Subject(r.Context()))
		assert.Equal(t, key.ID, auth.Actor(r.Context()))
		assert.Equal(t, auth.ActorAPIKey, auth.ActorKindFromContext(r.Context()))
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token.RevealString())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestChainMiddleware_SessionBeforeJWTSucceeds(t *testing.T) {
	root := []byte("0123456789abcdef0123456789abcdef")
	signer, err := session.NewSigner(root, "session")
	require.NoError(t, err)

	token, err := signer.Mint(session.Claims{
		UserID: testUserID, Tenant: "tenant-a", Role: "member",
		TokenVersion: 1, Exp: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)

	var jwtCalled atomic.Bool
	h := auth.ChainMiddleware(
		auth.NewSessionAuthenticator(session.Validator{Signer: signer}),
		auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
			jwtCalled.Store(true)
			return auth.Identity{UserID: testUserID}, nil
		}),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, testUserID, auth.UserID(r.Context()))
		assert.Equal(t, auth.ActorUser, auth.ActorKindFromContext(r.Context()))
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.False(t, jwtCalled.Load(), "session token must authenticate before JWT strategy runs")
}

func TestChainMiddleware_JWTBeforeSessionBlocksSessionToken(t *testing.T) {
	root := []byte("0123456789abcdef0123456789abcdef")
	signer, err := session.NewSigner(root, "session")
	require.NoError(t, err)

	token, err := signer.Mint(session.Claims{
		UserID: testUserID, Tenant: "tenant-a", Role: "member",
		TokenVersion: 1, Exp: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)

	h := auth.ChainMiddleware(
		auth.AuthenticatorFunc(func(*http.Request) (auth.Identity, error) {
			return auth.Identity{}, auth.ErrInvalidCredentials
		}),
		auth.NewSessionAuthenticator(session.Validator{Signer: signer}),
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}
