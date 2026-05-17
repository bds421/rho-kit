//go:build integration

package rediscache

import (
	"context"
	"testing"

	"github.com/bds421/rho-kit/data/cache/rediscache/v2"
	"github.com/bds421/rho-kit/data/v2/cache"
	"github.com/bds421/rho-kit/data/v2/cache/cachetest"
)

// TestRedisCache_Conformance runs the kit's cache.Cache
// conformance battery against rediscache.Cache. The harness's
// pollUntilHit handles Redis's synchronous Set visibility just
// as well as it handles Ristretto's asynchronous write buffer.
func TestRedisCache_Conformance(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Cache {
		client := redisClient(t)
		t.Cleanup(func() {
			_ = client.FlushDB(context.Background()).Err()
		})
		c, err := rediscache.NewCache(client, "conformance")
		if err != nil {
			t.Fatalf("NewCache: %v", err)
		}
		return c
	})
}
