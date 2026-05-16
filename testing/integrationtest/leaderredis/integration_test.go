//go:build integration

package leaderredis

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rlock "github.com/bds421/rho-kit/data/lock/redislock/v2"
	redislockle "github.com/bds421/rho-kit/infra/leaderelection/redislock/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
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

// One Elector wins; OnAcquired fires; cancelling the context returns the ctx error.
func TestElector_Run_AcquiresAndShutsDownOnCtxCancel(t *testing.T) {
	client := redisClient(t)
	key := uniqueKey(t, "leader")

	e := redislockle.New(client, key,
		redislockle.WithRetryInterval(50*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acquired := make(chan struct{}, 1)
	lost := atomic.Int64{}
	runErr := make(chan error, 1)

	go func() {
		runErr <- e.Run(ctx, leaderelection.Callbacks{
			OnAcquired: func(cbCtx context.Context) {
				select {
				case acquired <- struct{}{}:
				default:
				}
				<-cbCtx.Done()
			},
			OnLost: func() { lost.Add(1) },
		})
	}()

	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("OnAcquired was never invoked within 5s")
	}

	assert.True(t, e.IsLeader(), "IsLeader must return true after OnAcquired")

	cancel()
	select {
	case err := <-runErr:
		assert.ErrorIs(t, err, context.Canceled,
			"Run must propagate ctx.Err() on caller-initiated shutdown")
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s after ctx cancel")
	}
	assert.Equal(t, int64(1), lost.Load(),
		"OnLost must fire exactly once for the acquired term on shutdown")
	assert.False(t, e.IsLeader(), "IsLeader must be false after OnLost")
}

// A second Elector for the same key is blocked while the first holds the lock,
// then wins once the first goroutine releases.
//
// OnAcquired must BLOCK until leadership ends; returning immediately would
// cause the elector to loop and re-acquire, dropping the lock between
// iterations and letting e2 sneak in. The callback signature is "run work
// until ctx is cancelled, then return"; the tests model that by signalling
// on a one-shot chan, then blocking on cbCtx.Done().
func TestElector_TwoCompetingElectorsOnSameKey(t *testing.T) {
	client := redisClient(t)
	key := uniqueKey(t, "competing")

	// Use a short TTL so the second elector's wait stays bounded if the
	// first cancels uncleanly.
	locker1 := rlock.NewLocker(client, rlock.WithTTL(3*time.Second))
	locker2 := rlock.NewLocker(client, rlock.WithTTL(3*time.Second))

	e1 := redislockle.NewWithLocker(locker1, key, redislockle.WithRetryInterval(50*time.Millisecond))
	e2 := redislockle.NewWithLocker(locker2, key, redislockle.WithRetryInterval(50*time.Millisecond))

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	e1Acquired := make(chan struct{}, 1)
	e2Acquired := make(chan struct{}, 1)

	go func() {
		_ = e1.Run(ctx1, leaderelection.Callbacks{
			OnAcquired: func(cbCtx context.Context) {
				select {
				case e1Acquired <- struct{}{}:
				default:
				}
				<-cbCtx.Done()
			},
		})
	}()

	select {
	case <-e1Acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("first elector never acquired")
	}

	go func() {
		_ = e2.Run(ctx2, leaderelection.Callbacks{
			OnAcquired: func(cbCtx context.Context) {
				select {
				case e2Acquired <- struct{}{}:
				default:
				}
				<-cbCtx.Done()
			},
		})
	}()

	// e2 should be blocked: confirm it doesn't acquire while e1 is leader,
	// AND that e1 still reports leadership while e2 is waiting.
	select {
	case <-e2Acquired:
		t.Fatal("second elector acquired while first still held the lock")
	case <-time.After(500 * time.Millisecond):
	}
	assert.True(t, e1.IsLeader(), "first elector must still report leadership while holding the key")
	assert.False(t, e2.IsLeader(), "second elector must not report leadership while waiting")

	cancel1()

	// After e1 releases, e2 must eventually acquire AND e1 must transition
	// off the leader role.
	select {
	case <-e2Acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("second elector never acquired after first relinquished")
	}
	assert.True(t, e2.IsLeader(), "second elector must report leadership after acquiring")
	// e1.IsLeader transitions to false inside the cancelled Run before the
	// goroutine exits; allow up to a second for the transition to propagate
	// (Run returns ctx.Err() once OnLost has fired and the leader flag has flipped).
	assert.Eventually(t, func() bool { return !e1.IsLeader() }, 5*time.Second, 20*time.Millisecond,
		"cancelled first elector must transition IsLeader to false")
}
