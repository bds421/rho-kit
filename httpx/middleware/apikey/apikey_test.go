package apikey_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mw "github.com/bds421/rho-kit/httpx/v2/middleware/apikey"
	apikeycore "github.com/bds421/rho-kit/security/v2/apikey"
)

// issue mints a key, stores it, and returns the plaintext token.
func issue(t *testing.T, repo apikeycore.Repository, opts apikeycore.GenerateOptions) string {
	t.Helper()
	key, token, err := apikeycore.Generate(opts)
	require.NoError(t, err)
	require.NoError(t, repo.Insert(context.Background(), key))
	return token.RevealString()
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, ok := mw.OwnerFromContext(r)
		if !ok {
			http.Error(w, "no owner", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(owner))
	})
}

func TestMiddleware_BearerTokenAuthenticates(t *testing.T) {
	repo := apikeycore.NewMemoryRepository()
	token := issue(t, repo, apikeycore.GenerateOptions{Owner: "tenant-1"})
	h := mw.Middleware(mw.Config{Repository: repo})(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "tenant-1", rec.Body.String())
}

func TestMiddleware_XAPIKeyHeaderAuthenticates(t *testing.T) {
	repo := apikeycore.NewMemoryRepository()
	token := issue(t, repo, apikeycore.GenerateOptions{Owner: "tenant-2"})
	h := mw.Middleware(mw.Config{Repository: repo})(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "tenant-2", rec.Body.String())
}

func TestMiddleware_RejectsMissingMalformedAndWrong(t *testing.T) {
	repo := apikeycore.NewMemoryRepository()
	token := issue(t, repo, apikeycore.GenerateOptions{Owner: "o"})
	h := mw.Middleware(mw.Config{Repository: repo})(okHandler())

	cases := map[string]func(*http.Request){
		"missing":   func(r *http.Request) {},
		"malformed": func(r *http.Request) { r.Header.Set("Authorization", "Bearer not-a-valid-token") },
		"empty bearer": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer ")
		},
		"wrong secret": func(r *http.Request) {
			// Valid structure, wrong secret: same prefix+id, tampered tail.
			r.Header.Set("Authorization", "Bearer "+token+"x")
		},
		"unknown id": func(r *http.Request) {
			r.Header.Set("X-API-Key", "rho_"+"00000000-0000-0000-0000-000000000000"+"_deadbeef")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			mutate(req)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
		})
	}
}

func TestMiddleware_RejectsExpiredAndRevoked(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	repo := apikeycore.NewMemoryRepository()

	expiredTok := issue(t, repo, apikeycore.GenerateOptions{Owner: "o", Now: now, ExpiresAt: now.Add(time.Hour)})
	revokedTok := issue(t, repo, apikeycore.GenerateOptions{Owner: "o", Now: now})

	// Revoke the second key.
	id, _, err := apikeycore.Parse(revokedTok, apikeycore.DefaultPrefix)
	require.NoError(t, err)
	require.NoError(t, repo.Revoke(context.Background(), id, now))

	// Clock past expiry/revocation.
	h := mw.Middleware(mw.Config{Repository: repo, Now: func() time.Time { return now.Add(2 * time.Hour) }})(okHandler())

	for _, tok := range []string{expiredTok, revokedTok} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	}
}

func TestMiddleware_PanicsOnNilRepository(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil repository")
		}
	}()
	_ = mw.Middleware(mw.Config{})
}

func TestContextAccessors_AbsentWhenUnauthenticated(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, ok := mw.OwnerFromContext(req)
	assert.False(t, ok)
	_, ok = mw.KeyIDFromContext(req)
	assert.False(t, ok)
	_, ok = mw.ScopesFromContext(req)
	assert.False(t, ok)
}

func TestScopesFromContext_ReturnsCopy(t *testing.T) {
	repo := apikeycore.NewMemoryRepository()
	token := issue(t, repo, apikeycore.GenerateOptions{Owner: "o", Scopes: []string{"a", "b"}})

	var first []string
	h := mw.Middleware(mw.Config{Repository: repo})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := mw.ScopesFromContext(r)
		require.True(t, ok)
		first = got
		got[0] = "mutated"
		again, _ := mw.ScopesFromContext(r)
		assert.Equal(t, "a", again[0], "each call returns an independent copy")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "mutated", first[0])
}
