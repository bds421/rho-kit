package cache

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeCache_Close(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "close:",
		WithStaleTTL(5*time.Minute),
	)
	require.NoError(t, err)

	var calls atomic.Int32
	fn := func(ctx context.Context) (string, time.Duration, error) {
		n := calls.Add(1)
		if n == 1 {
			return "v1", 50 * time.Millisecond, nil
		}
		return "v2", 10 * time.Minute, nil
	}

	// Compute initial value.
	val, err := cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "v1", val)
	backend.Sync()

	// Wait for expiry to trigger a background refresh.
	time.Sleep(100 * time.Millisecond)
	_, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)

	// Close should cancel background work and wait for completion.
	err = cc.Close()
	require.NoError(t, err)
}

func TestComputeCache_CloseIdempotent(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "closeidem:")
	require.NoError(t, err)

	// Calling Close multiple times must not panic.
	err = cc.Close()
	require.NoError(t, err)

	err = cc.Close()
	require.NoError(t, err)
}

func TestComputeCache_GetOrComputeAfterClose(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "afterclose:")
	require.NoError(t, err)

	err = cc.Close()
	require.NoError(t, err)

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "should-not-run", 10 * time.Minute, nil
	}

	_, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCacheClosed)
}

func TestComputeCache_CloseDrainsForegroundCompute(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "fgclose:")
	require.NoError(t, err)

	started := make(chan struct{})
	fnDone := make(chan struct{})
	fn := func(ctx context.Context) (string, time.Duration, error) {
		close(started)
		<-ctx.Done()
		close(fnDone)
		return "", 0, ctx.Err()
	}

	// Start a foreground compute on a Background ctx so the caller has
	// no deadline of its own — only Close can unblock it.
	callerDone := make(chan error, 1)
	go func() {
		_, err := cc.GetOrCompute(context.Background(), "k", fn)
		callerDone <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("foreground compute did not start")
	}

	// Close must cancel the foreground compute and drain the goroutine.
	closed := make(chan error, 1)
	go func() { closed <- cc.Close() }()

	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return; foreground compute was not drained")
	}

	select {
	case <-fnDone:
	case <-time.After(time.Second):
		t.Fatal("foreground fn did not exit after Close")
	}

	select {
	case err := <-callerDone:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("foreground caller did not return after Close")
	}
}

func TestComputeCache_WithRefreshTimeout(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "rto:",
		WithStaleTTL(5*time.Minute),
		WithRefreshTimeout(50*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	var calls atomic.Int32
	fn := func(ctx context.Context) (string, time.Duration, error) {
		n := calls.Add(1)
		if n == 1 {
			return "v1", 50 * time.Millisecond, nil
		}
		// Second call blocks until context is cancelled or done.
		<-ctx.Done()
		return "", 0, ctx.Err()
	}

	// Compute initial value.
	val, err := cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "v1", val)
	backend.Sync()

	// Wait for expiry to trigger a background refresh.
	time.Sleep(100 * time.Millisecond)

	// This should serve stale and trigger a bg refresh that will time out.
	val, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "v1", val)

	// Wait for bg refresh to complete (should time out quickly).
	cc.Wait()
}
