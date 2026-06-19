package redislock_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redislock "github.com/bds421/rho-kit/data/lock/redislock/v2"
)

// blackHoleRedis starts a TCP listener that accepts connections and reads
// bytes but never replies. A Redis command sent to it blocks until the
// command's context deadline fires — simulating a stalled Redis node so we
// can drive an internal maxWait timeout to expire *during* the acquire
// command rather than during the inter-try delay.
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
			// Drain the request but never write a response.
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

// TestLocker_MaxWaitTimeoutDuringCommandIsContention asserts the documented
// WithMaxWait contract: when the internal maxWait timeout fires before
// redsync exhausts retries — including when it fires mid-command on the
// final try — Acquire returns (nil, false, nil) rather than a wrapped
// backend error. The caller's own context is never cancelled here, so this
// is "contention exhausted", not a caller-driven cancellation.
func TestLocker_MaxWaitTimeoutDuringCommandIsContention(t *testing.T) {
	addr := blackHoleRedis(t)
	client := redis.NewClient(&redis.Options{
		Addr:                  addr,
		ContextTimeoutEnabled: true,
		// Generous socket timeouts so the command is bounded by the
		// maxWait context, not by go-redis's own read/write deadlines.
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		DialTimeout:  2 * time.Second,
		// Single connection keeps the stalled command on the wire.
		PoolSize: 1,
	})
	t.Cleanup(func() { _ = client.Close() })

	// TTL is large enough that the inner per-command timeout
	// (expiry * timeoutFactor = 10s * 0.05 = 500ms) exceeds maxWait, so
	// the maxWait context is what cancels the command.
	lc := redislock.NewLocker(client,
		redislock.WithTTL(10*time.Second),
		redislock.WithMaxWait(150*time.Millisecond),
	)

	ctx := context.Background()
	start := time.Now()
	l, ok, err := lc.Acquire(ctx, "test:maxwait")
	elapsed := time.Since(start)

	// Internal maxWait expiry must surface as contention, not a backend error.
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, l)
	// Sanity: it actually waited on the stalled command rather than failing fast.
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond)
}
