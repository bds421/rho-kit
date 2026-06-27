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
		assert.Equal(t, testUserID, auth.UserID(r.Context()))
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