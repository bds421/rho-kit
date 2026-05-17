//go:build integration

package lockredis

import (
	"context"
	"testing"

	"github.com/bds421/rho-kit/data/lock/redislock/v2"
	"github.com/bds421/rho-kit/data/v2/lock"
	"github.com/bds421/rho-kit/data/v2/lock/locktest"
)

// TestRedisLock_Conformance runs the kit's lock.Locker
// conformance battery against redislock.
func TestRedisLock_Conformance(t *testing.T) {
	locktest.Run(t, func(t *testing.T) lock.Locker {
		client := redisClient(t)
		t.Cleanup(func() {
			_ = client.FlushDB(context.Background()).Err()
		})
		return redislock.NewLocker(client)
	})
}
