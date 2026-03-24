package concurrency

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// FanOut
// ---------------------------------------------------------------------------

func TestFanOut_AllSucceed(t *testing.T) {
	fns := []func(ctx context.Context) (int, error){
		func(_ context.Context) (int, error) { return 1, nil },
		func(_ context.Context) (int, error) { return 2, nil },
		func(_ context.Context) (int, error) { return 3, nil },
	}

	got, err := FanOut(context.Background(), fns)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3}, got)
}

func TestFanOut_OneFailsCancelsOthers(t *testing.T) {
	errBoom := errors.New("boom")

	started := make(chan struct{})
	fns := []func(ctx context.Context) (int, error){
		func(ctx context.Context) (int, error) {
			close(started)
			<-ctx.Done()
			return 0, ctx.Err()
		},
		func(_ context.Context) (int, error) {
			<-started // ensure first goroutine is running
			return 0, errBoom
		},
	}

	_, err := FanOut(context.Background(), fns)
	require.Error(t, err)
	assert.ErrorIs(t, err, errBoom)
}

func TestFanOut_PanicRecovery(t *testing.T) {
	fns := []func(ctx context.Context) (int, error){
		func(_ context.Context) (int, error) { panic("kaboom") },
	}

	_, err := FanOut(context.Background(), fns)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kaboom")
	assert.Contains(t, err.Error(), "panicked")
}

func TestFanOut_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	fns := []func(ctx context.Context) (int, error){
		func(ctx context.Context) (int, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		},
	}

	_, err := FanOut(ctx, fns)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFanOut_WithMaxGoroutines(t *testing.T) {
	var running atomic.Int32
	var peak atomic.Int32

	const limit = 2
	fns := make([]func(ctx context.Context) (int, error), 10)
	for i := range fns {
		fns[i] = func(_ context.Context) (int, error) {
			cur := running.Add(1)
			// Track peak concurrency.
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			running.Add(-1)
			return 0, nil
		}
	}

	got, err := FanOut(context.Background(), fns, WithMaxGoroutines(limit))
	require.NoError(t, err)
	assert.Len(t, got, 10)
	assert.LessOrEqual(t, peak.Load(), int32(limit))
}

func TestFanOut_ZeroFunctions(t *testing.T) {
	got, err := FanOut[int](context.Background(), nil)
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestFanOut_SingleFunction(t *testing.T) {
	fns := []func(ctx context.Context) (string, error){
		func(_ context.Context) (string, error) { return "only", nil },
	}

	got, err := FanOut(context.Background(), fns)
	require.NoError(t, err)
	assert.Equal(t, []string{"only"}, got)
}

// ---------------------------------------------------------------------------
// FanOutSettled
// ---------------------------------------------------------------------------

func TestFanOutSettled_AllSucceed(t *testing.T) {
	fns := []func(ctx context.Context) (int, error){
		func(_ context.Context) (int, error) { return 10, nil },
		func(_ context.Context) (int, error) { return 20, nil },
	}

	got := FanOutSettled(context.Background(), fns)
	require.Len(t, got, 2)
	assert.Equal(t, 10, got[0].Value)
	assert.NoError(t, got[0].Err)
	assert.Equal(t, 20, got[1].Value)
	assert.NoError(t, got[1].Err)
}

func TestFanOutSettled_OneFailsOthersComplete(t *testing.T) {
	errBad := errors.New("bad")
	var secondDone atomic.Bool

	fns := []func(ctx context.Context) (int, error){
		func(_ context.Context) (int, error) { return 0, errBad },
		func(_ context.Context) (int, error) {
			time.Sleep(20 * time.Millisecond) // ensure we outlive the failing one
			secondDone.Store(true)
			return 42, nil
		},
	}

	got := FanOutSettled(context.Background(), fns)
	require.Len(t, got, 2)

	assert.ErrorIs(t, got[0].Err, errBad)
	assert.True(t, secondDone.Load(), "second function must complete even though first failed")
	assert.Equal(t, 42, got[1].Value)
	assert.NoError(t, got[1].Err)
}

func TestFanOutSettled_PanicRecovery(t *testing.T) {
	fns := []func(ctx context.Context) (int, error){
		func(_ context.Context) (int, error) { return 1, nil },
		func(_ context.Context) (int, error) { panic("settled-boom") },
	}

	got := FanOutSettled(context.Background(), fns)
	require.Len(t, got, 2)

	assert.NoError(t, got[0].Err)
	assert.Equal(t, 1, got[0].Value)

	require.Error(t, got[1].Err)
	assert.Contains(t, got[1].Err.Error(), "settled-boom")
	assert.Contains(t, got[1].Err.Error(), "panicked")
}

func TestFanOutSettled_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fns := []func(ctx context.Context) (int, error){
		func(ctx context.Context) (int, error) {
			return 0, ctx.Err()
		},
	}

	got := FanOutSettled(ctx, fns)
	require.Len(t, got, 1)
	assert.ErrorIs(t, got[0].Err, context.Canceled)
}

func TestFanOutSettled_WithMaxGoroutines(t *testing.T) {
	var running atomic.Int32
	var peak atomic.Int32

	const limit = 3
	fns := make([]func(ctx context.Context) (int, error), 10)
	for i := range fns {
		fns[i] = func(_ context.Context) (int, error) {
			cur := running.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			running.Add(-1)
			return 0, nil
		}
	}

	got := FanOutSettled(context.Background(), fns, WithMaxGoroutines(limit))
	assert.Len(t, got, 10)
	assert.LessOrEqual(t, peak.Load(), int32(limit))
}

func TestFanOutSettled_ZeroFunctions(t *testing.T) {
	got := FanOutSettled[int](context.Background(), nil)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestFanOutSettled_SingleFunction(t *testing.T) {
	fns := []func(ctx context.Context) (string, error){
		func(_ context.Context) (string, error) { return "solo", nil },
	}

	got := FanOutSettled(context.Background(), fns)
	require.Len(t, got, 1)
	assert.Equal(t, "solo", got[0].Value)
	assert.NoError(t, got[0].Err)
}
