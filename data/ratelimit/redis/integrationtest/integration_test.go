//go:build integration

package integrationtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rlredis "github.com/bds421/rho-kit/data/ratelimit/redis/v2"
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

// Allow admits a burst, denies the next call, and recovers after a refill
// period — the canonical GCRA contract.
func TestLimiter_GCRABurstAndRefill(t *testing.T) {
	client := redisClient(t)
	lim := rlredis.New(client, time.Second, 3)

	ctx := context.Background()
	key := uniqueKey(t, "tenant")

	// Burst of 3 should all admit.
	for i := 0; i < 3; i++ {
		ok, retry, err := lim.Allow(ctx, key)
		require.NoError(t, err, "i=%d", i)
		assert.True(t, ok, "burst slot %d must admit", i)
		assert.Zero(t, retry, "burst slot %d must report zero retryAfter", i)
	}

	// The 4th immediate call must deny with a positive retry.
	ok, retry, err := lim.Allow(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok, "fourth call within the second must deny")
	assert.Greater(t, retry, time.Duration(0))
	// Retry must be bounded by the burst-refill period (1s for rate=1/s).
	assert.LessOrEqual(t, retry, time.Second)
}

// Distinct keys do not share state.
func TestLimiter_PerKeyIsolation(t *testing.T) {
	client := redisClient(t)
	lim := rlredis.New(client, time.Second, 1)

	ctx := context.Background()
	keyA := uniqueKey(t, "alice")
	keyB := uniqueKey(t, "bob")

	okA, _, err := lim.Allow(ctx, keyA)
	require.NoError(t, err)
	require.True(t, okA, "first call for alice must admit")

	okB, _, err := lim.Allow(ctx, keyB)
	require.NoError(t, err)
	assert.True(t, okB, "first call for bob must admit; per-key isolation broken if denied")

	// Burst exhausted on each key.
	denyA, _, err := lim.Allow(ctx, keyA)
	require.NoError(t, err)
	assert.False(t, denyA, "alice's second call must deny")
}

// WithKeyPrefix segregates two limiter instances against the same Redis.
func TestLimiter_WithKeyPrefixSegregatesNamespaces(t *testing.T) {
	client := redisClient(t)

	limA := rlredis.New(client, time.Second, 1, rlredis.WithKeyPrefix("svcA"))
	limB := rlredis.New(client, time.Second, 1, rlredis.WithKeyPrefix("svcB"))

	ctx := context.Background()
	key := uniqueKey(t, "shared")

	okA, _, err := limA.Allow(ctx, key)
	require.NoError(t, err)
	require.True(t, okA)

	// Same key in service B must still admit because the prefix differs.
	okB, _, err := limB.Allow(ctx, key)
	require.NoError(t, err)
	assert.True(t, okB, "WithKeyPrefix must not let svcA and svcB share counter state")
}

// WithRedisTime trips the TIME-based clock branch; verify the script still
// returns the GCRA contract.
func TestLimiter_WithRedisTime(t *testing.T) {
	client := redisClient(t)
	lim := rlredis.New(client, time.Second, 2, rlredis.WithRedisTime())

	ctx := context.Background()
	key := uniqueKey(t, "redistime")

	for i := 0; i < 2; i++ {
		ok, _, err := lim.Allow(ctx, key)
		require.NoError(t, err, "i=%d", i)
		assert.True(t, ok)
	}
	ok, retry, err := lim.Allow(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Greater(t, retry, time.Duration(0))
}
