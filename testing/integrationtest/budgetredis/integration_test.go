//go:build integration

package budgetredis

import (
	"context"
	"fmt"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	budgetredis "github.com/bds421/rho-kit/data/budget/redis/v2"
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

// Consume admits up to cap and denies the next call. Peek reflects remaining.
func TestBudget_ConsumeUntilCapThenDeny(t *testing.T) {
	client := redisClient(t)
	b := budgetredis.New(client, 5, time.Hour)

	ctx := context.Background()
	key := uniqueKey(t, "tenant")

	for i := int64(0); i < 5; i++ {
		ok, remaining, retry, err := b.Consume(ctx, key, 1)
		require.NoError(t, err, "i=%d", i)
		assert.True(t, ok, "Consume(1) at i=%d must admit", i)
		assert.Equal(t, 5-(i+1), remaining)
		assert.Zero(t, retry)
	}

	// Sixth must deny with a positive retryAfter (the rollover delay).
	ok, remaining, retry, err := b.Consume(ctx, key, 1)
	require.NoError(t, err)
	assert.False(t, ok, "Consume past cap must deny")
	assert.Equal(t, int64(0), remaining)
	assert.Greater(t, retry, time.Duration(0))
}

// Peek returns the full cap for unseen keys.
func TestBudget_PeekUnknownKeyReturnsFullCap(t *testing.T) {
	client := redisClient(t)
	b := budgetredis.New(client, 100, time.Hour)

	rem, err := b.Peek(context.Background(), uniqueKey(t, "unseen"))
	require.NoError(t, err)
	assert.Equal(t, int64(100), rem)
}

// Refund restores capacity within the same period, clamped at cap.
func TestBudget_RefundRestoresAndClampsAtCap(t *testing.T) {
	client := redisClient(t)
	b := budgetredis.New(client, 10, time.Hour)

	ctx := context.Background()
	key := uniqueKey(t, "refund")
	chargedAt := time.Now()

	_, _, _, err := b.Consume(ctx, key, 5)
	require.NoError(t, err)

	rem, err := b.Refund(ctx, key, 3, chargedAt)
	require.NoError(t, err)
	assert.Equal(t, int64(8), rem, "refund of 3 against 5-consumed cap-10 should give 8")

	// Refunding past cap must clamp.
	rem, err = b.Refund(ctx, key, 100, chargedAt)
	require.NoError(t, err)
	assert.Equal(t, int64(10), rem, "Refund must clamp at cap")
}

// WithKeyPrefix segregates two budgets against the same Redis.
func TestBudget_WithKeyPrefixSegregatesNamespaces(t *testing.T) {
	client := redisClient(t)
	bA := budgetredis.New(client, 1, time.Hour, budgetredis.WithKeyPrefix("svcA"))
	bB := budgetredis.New(client, 1, time.Hour, budgetredis.WithKeyPrefix("svcB"))

	ctx := context.Background()
	key := uniqueKey(t, "shared")

	okA, _, _, err := bA.Consume(ctx, key, 1)
	require.NoError(t, err)
	require.True(t, okA)

	okB, _, _, err := bB.Consume(ctx, key, 1)
	require.NoError(t, err)
	assert.True(t, okB, "WithKeyPrefix must isolate counter state across services")
}
