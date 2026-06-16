package redlock_test

import (
	"context"
	"net"
	"testing"
	"time"

	goredislib "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/lock/redislock/v2/redlock"
)

// blackHoleRedis starts a TCP listener that accepts connections and reads
// bytes but never replies. A Redis command sent to it blocks until the
// command's context deadline fires — simulating stalled Redis nodes so we
// can drive an internal maxWait timeout to expire *during* the acquire
// command on the final try rather than during the inter-try delay.
func blackHoleRedis(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 256)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func blackHoleClient(t *testing.T) goredislib.UniversalClient {
	t.Helper()
	c := goredislib.NewClient(&goredislib.Options{
		Addr:                  blackHoleRedis(t),
		ContextTimeoutEnabled: true,
		ReadTimeout:           5 * time.Second,
		WriteTimeout:          5 * time.Second,
		DialTimeout:           2 * time.Second,
		PoolSize:              1,
	})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestQuorumLocker_MaxWaitTimeoutDuringCommandIsContention asserts the
// documented WithMaxWait contract for the quorum locker: when the internal
// maxWait timeout fires before redsync exhausts retries — including
// mid-command on the final try — Acquire returns (nil, false, nil) rather
// than a wrapped backend error. The caller's own context is never cancelled.
func TestQuorumLocker_MaxWaitTimeoutDuringCommandIsContention(t *testing.T) {
	clients := []goredislib.UniversalClient{
		blackHoleClient(t),
		blackHoleClient(t),
		blackHoleClient(t),
	}

	// TTL large enough that the inner per-command timeout
	// (expiry * timeoutFactor = 10s * 0.05 = 500ms) exceeds maxWait, so
	// the maxWait context is what cancels the commands.
	q := redlock.NewQuorumLocker(clients,
		redlock.WithTTL(10*time.Second),
		redlock.WithMaxWait(150*time.Millisecond),
	)

	ctx := context.Background()
	start := time.Now()
	l, ok, err := q.Acquire(ctx, "kit/redlock/test/maxwait")
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, l)
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond)
}
