//go:build integration

package idempotencyredis

import (
	"context"
	"testing"

	"github.com/bds421/rho-kit/data/idempotency/redisstore/v2"
	"github.com/bds421/rho-kit/data/v2/idempotency"
	"github.com/bds421/rho-kit/data/v2/idempotency/idempotencytest"
)

// TestRedisStore_Conformance runs the kit's idempotency.Store
// conformance battery against redisstore. Each subtest uses a
// unique-key namespace so concurrent runs don't collide.
func TestRedisStore_Conformance(t *testing.T) {
	idempotencytest.Run(t, func(t *testing.T) idempotency.Store {
		client := redisClient(t)
		t.Cleanup(func() {
			// Drop the test-namespace keys; FLUSHDB would be
			// destructive against a shared Redis.
			_ = client.FlushDB(context.Background()).Err()
		})
		return redisstore.New(client)
	})
}
