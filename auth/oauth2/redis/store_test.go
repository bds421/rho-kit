package redis_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redisoauth "github.com/bds421/rho-kit/auth/oauth2/redis/v2"
	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/core/v2/secret"
)

func newStore(t *testing.T) (*miniredis.Miniredis, *redisoauth.Store) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return mr, redisoauth.New(client, redisoauth.WithPrefix("test:oidc:"))
}

func TestSessionRoundTripAndTTL(t *testing.T) {
	mr, store := newStore(t)
	session := oauth2.Session{SessionID: "s1", UserID: "user-1", AccessToken: secret.NewFromString("access"), RefreshToken: secret.NewFromString("refresh"), Expiry: time.Now().Add(time.Hour), Claims: map[string]any{"email": "a@example.com"}}
	require.NoError(t, store.Put(context.Background(), "s1", session, time.Minute))
	got, err := store.Get(context.Background(), "s1")
	require.NoError(t, err)
	assert.Equal(t, "user-1", got.UserID)
	assert.Equal(t, "access", got.AccessToken.RevealString())
	assert.Equal(t, "refresh", got.RefreshToken.RevealString())
	assert.Equal(t, "a@example.com", got.Claims["email"])
	mr.FastForward(time.Minute + time.Second)
	_, err = store.Get(context.Background(), "s1")
	assert.ErrorIs(t, err, oauth2.ErrSessionNotFound)
}

func TestStateNamespaceAndReplayDeletion(t *testing.T) {
	_, store := newStore(t)
	states := store.States()
	entry := oauth2.StateEntry{Nonce: "nonce", CodeVerifier: "verifier", RedirectTo: "/orders", CreatedAt: time.Now().UTC()}
	require.NoError(t, states.Put(context.Background(), "same", entry, time.Minute))
	require.NoError(t, store.Put(context.Background(), "same", oauth2.Session{UserID: "u"}, time.Minute))
	got, err := states.Get(context.Background(), "same")
	require.NoError(t, err)
	assert.Equal(t, entry.Nonce, got.Nonce)
	require.NoError(t, states.Delete(context.Background(), "same"))
	_, err = states.Get(context.Background(), "same")
	assert.ErrorIs(t, err, oauth2.ErrStateNotFound)
	_, err = store.Get(context.Background(), "same")
	require.NoError(t, err, "deleting a callback state must not delete a session with the same opaque id")
}

func TestInputAndBackendFailuresFailClosed(t *testing.T) {
	_, store := newStore(t)
	err := store.Put(context.Background(), "bad key", oauth2.Session{}, time.Minute)
	require.Error(t, err)
	err = store.Put(context.Background(), "good", oauth2.Session{}, 0)
	require.Error(t, err)
	_, err = store.Get(context.Background(), "missing")
	assert.True(t, errors.Is(err, oauth2.ErrSessionNotFound))
}
