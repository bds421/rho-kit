package apikey_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/authz/v2"
	mw "github.com/bds421/rho-kit/httpx/v2/middleware/apikey"
	apikeycore "github.com/bds421/rho-kit/security/v2/apikey"
)

// Scopes registered once for the scope-enforcement tests. Registration is
// process-global and idempotent across this test binary.
var (
	scopeRead  = authz.MustRegister("test.apikey.read", "read test resources")
	scopeWrite = authz.MustRegister("test.apikey.write", "write test resources")
)

func chain(repo apikeycore.Repository, required ...authz.Scope) http.Handler {
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mw.Middleware(mw.Config{Repository: repo})(mw.RequireScopes(required...)(final))
}

func TestRequireScopes_AllowsWhenKeyHasScope(t *testing.T) {
	repo := apikeycore.NewMemoryRepository()
	key, token, err := apikeycore.Generate(apikeycore.GenerateOptions{
		Owner:  "o",
		Scopes: []string{string(scopeRead), string(scopeWrite)},
	})
	require.NoError(t, err)
	require.NoError(t, repo.Insert(context.Background(), key))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token.RevealString())
	rec := httptest.NewRecorder()
	chain(repo, scopeRead).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireScopes_ForbidsWhenScopeMissing(t *testing.T) {
	repo := apikeycore.NewMemoryRepository()
	key, token, err := apikeycore.Generate(apikeycore.GenerateOptions{
		Owner:  "o",
		Scopes: []string{string(scopeRead)},
	})
	require.NoError(t, err)
	require.NoError(t, repo.Insert(context.Background(), key))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token.RevealString())
	rec := httptest.NewRecorder()
	chain(repo, scopeWrite).ServeHTTP(rec, req) // key lacks write

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRequireScopes_UnauthorizedWithoutAuthentication(t *testing.T) {
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// RequireScopes alone, no Middleware in front: no scopes on context.
	h := mw.RequireScopes(scopeRead)(final)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireScopes_PanicsOnUnregisteredScope(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on unregistered scope")
		}
	}()
	_ = mw.RequireScopes(authz.Scope("never.registered.scope"))
}
