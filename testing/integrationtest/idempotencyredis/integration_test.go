//go:build integration

package idempotencyredis

import (
	"context"
	"fmt"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/idempotency/redisstore/v2"
	"github.com/bds421/rho-kit/data/v2/idempotency"
	"github.com/bds421/rho-kit/infra/redis/redistest/v2"
	"github.com/bds421/rho-kit/infra/redis/v2"
)

func redisClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	url := redistest.Start(t)
	opts, err := goredis.ParseURL(url)
	require.NoError(t, err)
	conn, err := redis.Connect(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Cleanup(func() { redistest.FlushDB(t) })
	return conn.Client()
}

func uniqueKey(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s:%d", prefix, time.Now().UnixNano())
}

func sampleResponse() idempotency.CachedResponse {
	return idempotency.CachedResponse{
		StatusCode: 200,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"ok":true}`),
	}
}

// TryLock-then-Set-then-Get is the happy-path lifecycle.
func TestStore_LockSetGet_Lifecycle(t *testing.T) {
	client := redisClient(t)
	s := redisstore.New(client)

	ctx := context.Background()
	key := uniqueKey(t, "req")
	fp := []byte("fingerprint-1")

	token, fpMismatch, ok, err := s.TryLock(ctx, key, fp, 5*time.Second)
	require.NoError(t, err)
	require.True(t, ok, "first TryLock must acquire on a fresh key")
	require.False(t, fpMismatch)
	require.NotEmpty(t, token)

	resp := sampleResponse()
	require.NoError(t, s.Set(ctx, key, token, resp, 5*time.Second))

	got, fpMismatch, err := s.Get(ctx, key, fp)
	require.NoError(t, err)
	require.False(t, fpMismatch)
	require.NotNil(t, got)
	assert.Equal(t, resp.StatusCode, got.StatusCode)
	assert.Equal(t, resp.Body, got.Body)
}

// TryLock on a key that already holds a lock with the same fingerprint
// returns ("", false, false, nil) — caller should send a 409 Conflict.
func TestStore_TryLock_SameFingerprintReturnsContention(t *testing.T) {
	client := redisClient(t)
	s := redisstore.New(client)

	ctx := context.Background()
	key := uniqueKey(t, "contend")
	fp := []byte("fp-1")

	_, _, ok, err := s.TryLock(ctx, key, fp, 5*time.Second)
	require.NoError(t, err)
	require.True(t, ok)

	token2, fpMismatch, ok2, err := s.TryLock(ctx, key, fp, 5*time.Second)
	require.NoError(t, err)
	assert.Empty(t, token2)
	assert.False(t, fpMismatch)
	assert.False(t, ok2, "second TryLock with same fingerprint must signal contention, not acquire")
}

// TryLock on a held key with a *different* fingerprint signals 422.
func TestStore_TryLock_DifferentFingerprintFlagsMismatch(t *testing.T) {
	client := redisClient(t)
	s := redisstore.New(client)

	ctx := context.Background()
	key := uniqueKey(t, "mismatch")

	_, _, ok, err := s.TryLock(ctx, key, []byte("fp-A"), 5*time.Second)
	require.NoError(t, err)
	require.True(t, ok)

	_, fpMismatch, ok2, err := s.TryLock(ctx, key, []byte("fp-B"), 5*time.Second)
	require.NoError(t, err)
	assert.False(t, ok2)
	assert.True(t, fpMismatch, "fingerprint mismatch must be surfaced for 422 mapping")
}

// Get returns (nil, false, nil) for an unknown key.
func TestStore_Get_MissingKey(t *testing.T) {
	client := redisClient(t)
	s := redisstore.New(client)

	got, fpMismatch, err := s.Get(context.Background(), uniqueKey(t, "missing"), []byte("fp"))
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.False(t, fpMismatch)
}

// Set with a stale (expired) token returns ErrLockLost.
func TestStore_Set_StaleTokenReturnsLockLost(t *testing.T) {
	client := redisClient(t)
	s := redisstore.New(client)

	ctx := context.Background()
	key := uniqueKey(t, "stale")

	// Use a tiny TTL so the lock expires before our Set call.
	token, _, ok, err := s.TryLock(ctx, key, []byte("fp"), 50*time.Millisecond)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEmpty(t, token)

	time.Sleep(120 * time.Millisecond)

	err = s.Set(ctx, key, token, sampleResponse(), 5*time.Second)
	assert.ErrorIs(t, err, idempotency.ErrLockLost,
		"Set after TTL expiry must return ErrLockLost")
}

// WithKeyPrefix segregates two stores against the same Redis.
func TestStore_WithKeyPrefixSegregatesNamespaces(t *testing.T) {
	client := redisClient(t)
	sA := redisstore.New(client, redisstore.WithKeyPrefix("svcA:"))
	sB := redisstore.New(client, redisstore.WithKeyPrefix("svcB:"))

	ctx := context.Background()
	key := uniqueKey(t, "shared")

	_, _, ok, err := sA.TryLock(ctx, key, []byte("fp"), 5*time.Second)
	require.NoError(t, err)
	require.True(t, ok)

	// Same key in service B must still be acquirable.
	_, _, ok, err = sB.TryLock(ctx, key, []byte("fp"), 5*time.Second)
	require.NoError(t, err)
	assert.True(t, ok, "WithKeyPrefix must namespace counter state across services")
}
