package redis_test

import (
	"bytes"
	"context"
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

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func newEncryptedStore(t *testing.T, mr *miniredis.Miniredis, keys ...redisoauth.EncryptionKey) *redisoauth.Store {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	opts := []redisoauth.Option{redisoauth.WithPrefix("test:oidc:")}
	if len(keys) > 0 {
		opts = append(opts, redisoauth.WithEncryption(keys...))
	}
	return redisoauth.New(client, opts...)
}

func testSession() oauth2.Session {
	return oauth2.Session{
		SessionID: "s1", UserID: "user-1",
		AccessToken: secret.NewFromString("access-secret"), RefreshToken: secret.NewFromString("refresh-secret"),
		IDToken: secret.NewFromString("id-token-secret"), Expiry: time.Now().Add(time.Hour),
		Claims: map[string]any{"email": "a@example.com"},
	}
}

func TestEncryption_RoundTripHidesTokens(t *testing.T) {
	mr := miniredis.RunT(t)
	store := newEncryptedStore(t, mr, redisoauth.EncryptionKey{ID: "k1", Key: key(1)})
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "s1", testSession(), time.Minute))
	require.NoError(t, store.States().Put(ctx, "st1", oauth2.StateEntry{Nonce: "nonce-secret", CodeVerifier: "verifier-secret"}, time.Minute))

	for _, k := range []string{"test:oidc:session:s1", "test:oidc:state:st1"} {
		raw, err := mr.Get(k)
		require.NoError(t, err)
		for _, plain := range []string{"access-secret", "refresh-secret", "id-token-secret", "a@example.com", "nonce-secret", "verifier-secret"} {
			assert.NotContains(t, raw, plain, "%s stores plaintext", k)
		}
		assert.True(t, len(raw) > 4 && raw[:4] == "rk1.", "%s not sealed", k)
	}

	got, err := store.Get(ctx, "s1")
	require.NoError(t, err)
	assert.Equal(t, "access-secret", got.AccessToken.RevealString())
	assert.Equal(t, "refresh-secret", got.RefreshToken.RevealString())
	assert.Equal(t, "id-token-secret", got.IDToken.RevealString())
	assert.Equal(t, "a@example.com", got.Claims["email"])
	state, err := store.States().Get(ctx, "st1")
	require.NoError(t, err)
	assert.Equal(t, "verifier-secret", state.CodeVerifier)
}

func TestEncryption_KeyRotation(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()
	old := redisoauth.EncryptionKey{ID: "k1", Key: key(1)}
	next := redisoauth.EncryptionKey{ID: "k2", Key: key(2)}

	require.NoError(t, newEncryptedStore(t, mr, old).Put(ctx, "s1", testSession(), time.Minute))

	rotated := newEncryptedStore(t, mr, next, old)
	got, err := rotated.Get(ctx, "s1")
	require.NoError(t, err, "older key must still decrypt")
	assert.Equal(t, "access-secret", got.AccessToken.RevealString())
	require.NoError(t, rotated.Put(ctx, "s2", testSession(), time.Minute))
	raw, err := mr.Get("test:oidc:session:s2")
	require.NoError(t, err)
	assert.Equal(t, "rk1.k2.", raw[:7], "first key encrypts")

	// Old key removed: its entries read as absent, new ones still work.
	retired := newEncryptedStore(t, mr, next)
	_, err = retired.Get(ctx, "s1")
	assert.ErrorIs(t, err, oauth2.ErrSessionNotFound)
	_, err = retired.Get(ctx, "s2")
	require.NoError(t, err)
}

func TestEncryption_PlaintextAndTamper(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()
	require.NoError(t, newEncryptedStore(t, mr).Put(ctx, "legacy", testSession(), time.Minute))
	require.NoError(t, newEncryptedStore(t, mr).States().Put(ctx, "legacy", oauth2.StateEntry{Nonce: "n"}, time.Minute))
	encrypted := newEncryptedStore(t, mr, redisoauth.EncryptionKey{ID: "k1", Key: key(1)})
	_, err := encrypted.Get(ctx, "legacy")
	assert.ErrorIs(t, err, oauth2.ErrSessionNotFound, "unencrypted legacy entry reads as absent")
	_, err = encrypted.States().Get(ctx, "legacy")
	assert.ErrorIs(t, err, oauth2.ErrStateNotFound)

	// A sealed value moved to another key fails authentication (AAD binding).
	require.NoError(t, encrypted.Put(ctx, "s1", testSession(), time.Minute))
	raw, err := mr.Get("test:oidc:session:s1")
	require.NoError(t, err)
	require.NoError(t, mr.Set("test:oidc:session:s2", raw))
	_, err = encrypted.Get(ctx, "s2")
	require.Error(t, err)
	assert.NotErrorIs(t, err, oauth2.ErrSessionNotFound)

	// Flipped ciphertext byte fails authentication.
	tampered := []byte(raw)
	tampered[len(tampered)-1] ^= 0xff
	require.NoError(t, mr.Set("test:oidc:session:s1", string(tampered)))
	_, err = encrypted.Get(ctx, "s1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, oauth2.ErrSessionNotFound)
}

func TestWithEncryption_Panics(t *testing.T) {
	cases := map[string][]redisoauth.EncryptionKey{
		"no keys":       nil,
		"empty id":      {{ID: "", Key: key(1)}},
		"bad id":        {{ID: "k.1", Key: key(1)}},
		"short key":     {{ID: "k1", Key: key(1)[:16]}},
		"duplicate ids": {{ID: "k1", Key: key(1)}, {ID: "k1", Key: key(2)}},
	}
	for name, keys := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, func() { redisoauth.WithEncryption(keys...) })
		})
	}
}

func TestSession_IDTokenPersistedWithoutEncryption(t *testing.T) {
	_, store := newStore(t)
	require.NoError(t, store.Put(context.Background(), "s1", testSession(), time.Minute))
	got, err := store.Get(context.Background(), "s1")
	require.NoError(t, err)
	require.NotNil(t, got.IDToken)
	assert.Equal(t, "id-token-secret", got.IDToken.RevealString())
}
