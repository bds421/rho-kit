package redislock

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	rlock "github.com/bds421/rho-kit/data/lock/redislock/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

// TestRun_DrainTimeoutReturnsImmediatelyWithoutReacquire pins L141:
// when ErrCallbackDrainTimeout is observed, Run() must return
// immediately rather than retrying acquire. Retrying would risk a
// within-process double-leader because the orphan OnAcquired goroutine
// from the previous term is still running with the resources it
// acquired.
//
// Setup: miniredis-backed locker; OnAcquired blocks forever ignoring
// ctx. The drain timeout fires; the elector reports lost; the kit
// observes ErrCallbackDrainTimeout and Run returns it. The test
// verifies the elector did NOT re-acquire by counting how many times
// OnAcquired was invoked (must be exactly 1).
func TestRun_DrainTimeoutReturnsImmediatelyWithoutReacquire(t *testing.T) {
	if testing.Short() {
		t.Skip("miniredis-backed integration test is skipped under -short")
	}
	mr := miniredis.RunT(t)
	t.Cleanup(mr.Close)

	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	// Construct the real locker against miniredis. Short TTL so the
	// lost-leadership path triggers quickly when we revoke the lock.
	locker := rlock.NewLocker(client, rlock.WithTTL(50*time.Millisecond))

	e := NewWithLocker(locker, "test-l141",
		WithRenewInterval(10*time.Millisecond),
		WithRetryInterval(20*time.Millisecond),
		WithCallbackDrainTimeout(75*time.Millisecond),
		WithCallbackDrainWarnInterval(time.Hour), // suppress drain warnings
	)

	var onAcquiredCalls atomic.Int32
	cbStarted := make(chan struct{}, 1)
	cb := leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			onAcquiredCalls.Add(1)
			select {
			case cbStarted <- struct{}{}:
			default:
			}
			// Uncooperative — ignore ctx.Done() entirely. The kit's
			// drain timeout is the only thing that should unstick us
			// from Run's perspective.
			select {}
		},
	}

	// Run on a worker goroutine; we observe its return below. The
	// outer ctx is generous — Run should return on its own via the
	// strict-stop semantics, not because the outer ctx fired.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- e.Run(ctx, cb)
	}()

	// Wait for the callback to actually start so we know the test is
	// exercising the drain path, not a startup race.
	select {
	case <-cbStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("OnAcquired never started — miniredis acquire failed")
	}

	// Once the callback is running, revoke the lock externally so the
	// renew tick reports `extend ok = false` → triggers the drain
	// path. miniredis lets us delete the key directly.
	mr.Del("test-l141")

	// Run must return ErrCallbackDrainTimeout (no retry).
	select {
	case err := <-runDone:
		require.Error(t, err, "Run must return on drain timeout")
		require.True(t, errors.Is(err, ErrCallbackDrainTimeout),
			"Run must surface ErrCallbackDrainTimeout, got: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s — retry loop may still be running, contradicting L141 strict-stop semantics")
	}

	// Hold for a small window after Run returns. If retry semantics
	// were still in play, the elector would re-acquire (miniredis
	// allows it now that the lock is gone) and OnAcquired would fire
	// again — pushing the call count above 1.
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, int32(1), onAcquiredCalls.Load(),
		"OnAcquired must be called exactly once; a second call would mean Run retried after ErrCallbackDrainTimeout")
}
