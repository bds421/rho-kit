package cache

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestComputeCache_WaitDrainsAbandonedLeader verifies that Wait blocks until
// the singleflight leader's compute goroutine finishes, even when the caller
// that started it abandoned early via its own context cancellation.
//
// Before the fix, foregroundWg tracked the calling goroutine rather than the
// DoChan-spawned leader closure. When the caller exited via ctx.Done(),
// foregroundWg.Done() fired while executeCompute was still in flight, so
// Wait() returned before the compute (and its backend.Set) completed —
// contradicting the documented "Close can wait for it to drain" contract.
func TestComputeCache_WaitDrainsAbandonedLeader(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "drain:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })

	started := make(chan struct{})
	release := make(chan struct{})
	var computeFinished atomic.Bool

	// fn ignores ctx (the abandoned-leader case the finding describes): it
	// runs to completion regardless of the caller's cancellation.
	fn := func(ctx context.Context) (string, time.Duration, error) {
		close(started)
		<-release
		computeFinished.Store(true)
		return "v", time.Minute, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	callerDone := make(chan struct{})
	go func() {
		defer close(callerDone)
		_, _ = cc.GetOrCompute(ctx, "k", fn)
	}()

	// Wait for the leader compute to actually start.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("leader compute did not start")
	}

	// Abandon the caller while the leader compute is mid-flight.
	cancel()
	select {
	case <-callerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("caller did not return after ctx cancellation")
	}

	// Wait must block until the leader compute goroutine drains. Run it in a
	// goroutine so the test can release the compute and observe ordering.
	waitReturned := make(chan struct{})
	go func() {
		cc.Wait()
		close(waitReturned)
	}()

	// Wait must NOT have returned yet — the compute is still blocked.
	select {
	case <-waitReturned:
		t.Fatal("Wait returned before the abandoned leader compute finished")
	case <-time.After(100 * time.Millisecond):
		// expected: still blocked
	}

	// Let the compute finish.
	close(release)

	select {
	case <-waitReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after the leader compute finished")
	}

	require.True(t, computeFinished.Load(),
		"Wait must observe the leader compute as finished once it returns")
}
