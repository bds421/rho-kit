package redis_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/core/v2/secret"
)

func TestUpdate_CompareAndSet(t *testing.T) {
	_, store := newStore(t)
	ctx := context.Background()
	ok, err := store.Update(ctx, "s1", 0, oauth2.Session{UserID: "u"}, time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "missing entry must not be created")
	_, err = store.Get(ctx, "s1")
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound)

	require.NoError(t, store.Put(ctx, "s1", oauth2.Session{UserID: "u", Version: 3}, time.Minute))
	ok, err = store.Update(ctx, "s1", 2, oauth2.Session{UserID: "u", Version: 4}, time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "stale version must not win")
	ok, err = store.Update(ctx, "s1", 3, oauth2.Session{UserID: "u", Version: 4}, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
	got, err := store.Get(ctx, "s1")
	require.NoError(t, err)
	assert.Equal(t, uint64(4), got.Version)
}

// TestRefresh_LogoutBetweenReadAndWriteStaysDeleted interleaves a logout
// with an in-flight token refresh against the Redis store: the provider
// handler deletes the session after the refresh read it, and the refreshed
// tokens must not recreate it.
func TestRefresh_LogoutBetweenReadAndWriteStaysDeleted(t *testing.T) {
	_, store := newStore(t)
	ctx := context.Background()
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			})
		case "/token":
			require.NoError(t, store.Delete(ctx, "s1")) // logout lands mid-refresh
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-2", "token_type": "Bearer", "refresh_token": "refresh-2", "expires_in": 3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	issuer = srv.URL

	client, err := oauth2.NewClient(ctx, oauth2.Config{Issuer: issuer, ClientID: "web", RedirectURL: "https://app.example.test/oauth/callback"},
		oauth2.WithSessionStore(store), oauth2.WithStateStore(store.States()), oauth2.WithTokenRefresh(time.Minute))
	require.NoError(t, err)
	require.NoError(t, store.Put(ctx, "s1", oauth2.Session{
		SessionID: "s1", UserID: "user-1", AccessToken: secret.NewFromString("access-1"),
		RefreshToken: secret.NewFromString("refresh-1"), Expiry: time.Now().Add(-time.Minute),
	}, time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: client.CookieName(), Value: "s1"})
	_, err = client.SessionFromRequest(ctx, req)
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound)
	_, err = store.Get(ctx, "s1")
	assert.ErrorIs(t, err, oauth2.ErrSessionNotFound, "refresh must not resurrect a logged-out session")
}
