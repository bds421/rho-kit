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

// TestMiddleware_UnknownIDAndBadSecretAreIndistinguishable verifies the
// contract documented in doc.go: the middleware never reveals whether a key id
// exists. An unknown id and a known id with a wrong secret must yield the exact
// same 401 response. The unknown-id path additionally runs a dummy Verify so
// it performs the same constant-time hash comparison as the bad-secret path,
// keeping the two indistinguishable by timing as well as by body.
func TestMiddleware_UnknownIDAndBadSecretAreIndistinguishable(t *testing.T) {
	repo := apikeycore.NewMemoryRepository()
	token := issue(t, repo, apikeycore.GenerateOptions{Owner: "o"})
	h := mw.Middleware(mw.Config{Repository: repo})(okHandler())

	// Known id, wrong secret: same prefix+id as a stored key, tampered tail.
	badSecretReq := httptest.NewRequest(http.MethodGet, "/", nil)
	badSecretReq.Header.Set("Authorization", "Bearer "+token+"x")
	badSecretRec := httptest.NewRecorder()
	h.ServeHTTP(badSecretRec, badSecretReq)

	// Unknown id: well-formed token whose id is not in the repository.
	unknownReq := httptest.NewRequest(http.MethodGet, "/", nil)
	unknownReq.Header.Set("Authorization", "Bearer rho_00000000-0000-0000-0000-000000000000_deadbeef")
	unknownRec := httptest.NewRecorder()
	h.ServeHTTP(unknownRec, unknownReq)

	require.Equal(t, http.StatusUnauthorized, badSecretRec.Code)
	assert.Equal(t, badSecretRec.Code, unknownRec.Code,
		"unknown id and bad secret must share the same status code")
	assert.Equal(t, badSecretRec.Body.String(), unknownRec.Body.String(),
		"unknown id and bad secret must share the same response body so id existence does not leak")
}

// TestMiddleware_UnknownIDNeverAuthenticates guards the dummy-Verify on the
// repository-miss path: no matter what well-formed secret is presented for an
// unknown id, the request must be rejected. A regression that let the dummy
// key (or any default key) authenticate would surface here.
func TestMiddleware_UnknownIDNeverAuthenticates(t *testing.T) {
	repo := apikeycore.NewMemoryRepository()
	h := mw.Middleware(mw.Config{Repository: repo})(okHandler())

	for _, secret := range []string{"deadbeef", "", "x"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", "rho_00000000-0000-0000-0000-000000000000_"+secret)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code,
			"unknown id with secret %q must be rejected", secret)
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
