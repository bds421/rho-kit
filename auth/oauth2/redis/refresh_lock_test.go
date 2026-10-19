package redis_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redisoauth "github.com/bds421/rho-kit/auth/oauth2/redis/v2"
	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/core/v2/secret"
)

// newTokenIssuer serves discovery plus a refresh-grant token endpoint.
func newTokenIssuer(t *testing.T, token http.HandlerFunc) string {
	t.Helper()
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			})
		case "/token":
			token(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	issuer = srv.URL
	return issuer
}

func writeRefreshed(w http.ResponseWriter, access string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": access, "token_type": "Bearer", "refresh_token": "refresh-2", "expires_in": 3600,
	})
}

func newReplica(t *testing.T, issuer string, store *redisoauth.Store, opts ...oauth2.Option) *oauth2.Client {
	t.Helper()
	opts = append([]oauth2.Option{
		oauth2.WithSessionStore(store), oauth2.WithStateStore(store.States()), oauth2.WithTokenRefresh(time.Minute),
	}, opts...)
	client, err := oauth2.NewClient(context.Background(), oauth2.Config{
		Issuer: issuer, ClientID: "web", RedirectURL: "https://app.example.test/oauth/callback",
	}, opts...)
	require.NoError(t, err)
	return client
}

func seedRefreshable(t *testing.T, store *redisoauth.Store, expiry time.Time) {
	t.Helper()
	require.NoError(t, store.Put(context.Background(), "s1", oauth2.Session{
		SessionID: "s1", UserID: "user-1", AccessToken: secret.NewFromString("access-1"),
		RefreshToken: secret.NewFromString("refresh-1"), Expiry: expiry,
	}, time.Hour))
}

func readSession(client *oauth2.Client) (oauth2.Session, error) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: client.CookieName(), Value: "s1"})
	return client.SessionFromRequest(context.Background(), req)
}

func TestTryLockRefresh_ExclusiveAndTokenBound(t *testing.T) {
	mr, store := newStore(t)
	ctx := context.Background()
	release, ok, err := store.TryLockRefresh(ctx, "s1", time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, err = store.TryLockRefresh(ctx, "s1", time.Second)
	require.NoError(t, err)
	assert.False(t, ok, "a held lock is exclusive")
	_, ok, err = store.TryLockRefresh(ctx, "s2", time.Second)
	require.NoError(t, err)
	assert.True(t, ok, "locks are per session")

	// The lock expires; a new holder takes it; the stale release must not free it.
	mr.FastForward(2 * time.Second)
	_, ok, err = store.TryLockRefresh(ctx, "s1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, release(ctx))
	_, ok, err = store.TryLockRefresh(ctx, "s1", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "a stale holder must not release another holder's lock")

	_, _, err = store.TryLockRefresh(ctx, "bad key", time.Minute)
	require.Error(t, err)
}

func TestRefreshLock_WaitsForOtherReplicaAndUsesItsTokens(t *testing.T) {
	_, store := newStore(t)
	var calls atomic.Int64
	issuer := newTokenIssuer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeRefreshed(w, "access-local")
	})
	client := newReplica(t, issuer, store)
	seedRefreshable(t, store, time.Now().Add(-time.Second))

	// Replica A holds the lock while it refreshes.
	release, ok, err := store.TryLockRefresh(context.Background(), "s1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	done := make(chan oauth2.Session, 1)
	go func() {
		sess, err := readSession(client)
		assert.NoError(t, err)
		done <- sess
	}()
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int64(0), calls.Load(), "replica B must not redeem the refresh token while A holds the lock")

	// Replica A stores its refreshed tokens and releases.
	ok, err = store.Update(context.Background(), "s1", 0, oauth2.Session{
		SessionID: "s1", UserID: "user-1", AccessToken: secret.NewFromString("access-from-a"),
		RefreshToken: secret.NewFromString("refresh-2"), Expiry: time.Now().Add(time.Hour), Version: 1,
	}, time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, release(context.Background()))

	select {
	case sess := <-done:
		assert.Equal(t, "access-from-a", sess.AccessToken.RevealString())
	case <-time.After(5 * time.Second):
		t.Fatal("replica B did not pick up replica A's tokens")
	}
	assert.Equal(t, int64(0), calls.Load())
}

func TestRefreshLock_ConcurrentReplicasRedeemOnce(t *testing.T) {
	_, store := newStore(t)
	var calls atomic.Int64
	issuer := newTokenIssuer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(200 * time.Millisecond)
		writeRefreshed(w, "access-2")
	})
	replicas := []*oauth2.Client{newReplica(t, issuer, store), newReplica(t, issuer, store), newReplica(t, issuer, store)}
	seedRefreshable(t, store, time.Now().Add(-time.Second))

	var wg sync.WaitGroup
	for _, replica := range replicas {
		wg.Go(func() {
			sess, err := readSession(replica)
			if assert.NoError(t, err) {
				assert.Equal(t, "access-2", sess.AccessToken.RevealString())
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int64(1), calls.Load(), "the refresh token must be redeemed once across replicas")
}

func TestRefreshLock_WaitTimeoutIsTransient(t *testing.T) {
	_, store := newStore(t)
	var calls atomic.Int64
	issuer := newTokenIssuer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeRefreshed(w, "access-2")
	})
	client := newReplica(t, issuer, store, oauth2.WithRefreshLockWait(150*time.Millisecond))
	_, ok, err := store.TryLockRefresh(context.Background(), "s1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Still-valid access token: served unchanged.
	seedRefreshable(t, store, time.Now().Add(30*time.Second))
	sess, err := readSession(client)
	require.NoError(t, err)
	assert.Equal(t, "access-1", sess.AccessToken.RevealString())

	// Expired access token: ends like any transient failure (no grace).
	seedRefreshable(t, store, time.Now().Add(-time.Second))
	_, err = readSession(client)
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound)
	assert.Equal(t, int64(0), calls.Load())
}
