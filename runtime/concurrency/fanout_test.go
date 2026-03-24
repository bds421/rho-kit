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
// PanicError
// ---------------------------------------------------------------------------

func TestPanicError_ErrorMessage(t *testing.T) {
	err := &PanicError{Index: 3, Value: "oops", Stack: "fake stack"}
	assert.Equal(t, "concurrency: goroutine 3 panicked: oops", err.Error())
}

func TestFanOut_PanicProducesPanicError(t *testing.T) {
	_, err := FanOut(context.Background(), []func(ctx context.Context) (int, error){
		func(_ context.Context) (int, error) { panic("check-type") },
	})
	require.Error(t, err)

	var pe *PanicError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, 0, pe.Index)
	assert.Equal(t, "check-type", pe.Value)
	assert.NotEmpty(t, pe.Stack)
}

func TestPanicError_Unwrap_WithErrorValue(t *testing.T) {
	inner := errors.New("root cause")
	pe := &PanicError{Index: 0, Value: inner, Stack: "fake"}
	assert.Equal(t, inner, pe.Unwrap())
	assert.ErrorIs(t, pe, inner)
}

func TestPanicError_Unwrap_WithNonErrorValue(t *testing.T) {
	pe := &PanicError{Index: 0, Value: "just a string", Stack: "fake"}
	assert.Nil(t, pe.Unwrap())
}

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

func TestFanOut_PanicWithError_UnwrapsViaErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	_, err := FanOut(context.Background(), []func(ctx context.Context) (int, error){
		func(_ context.Context) (int, error) {
			panic(sentinel)
		},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

func TestFanOut_PanicRecovery(t *testing.T) {
	fns := []func(ctx context.Context) (int, error){
		func(_ context.Context) (int, error) { panic("kaboom") },
	}

	_, err := FanOut(context.Background(), fns)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kaboom")
	assert.Contains(t, err.Error(), "panicked")

	var pe *PanicError
	require.ErrorAs(t, err, &pe)
	assert.NotEmpty(t, pe.Stack)
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

	var pe *PanicError
	require.ErrorAs(t, got[1].Err, &pe)
	assert.Equal(t, 1, pe.Index)
	assert.NotEmpty(t, pe.Stack)
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

func TestFanOutSettled_ValueZeroedOnError(t *testing.T) {
	fns := []func(ctx context.Context) (int, error){
		func(_ context.Context) (int, error) { return 42, errors.New("fail") },
	}

	got := FanOutSettled(context.Background(), fns)
	require.Len(t, got, 1)
	require.Error(t, got[0].Err)
	assert.Zero(t, got[0].Value, "Value must be zero when Err is non-nil")
}

func TestFanOutSettled_ContextAwareSemaphore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	acquired := make(chan struct{})
	blocker := make(chan struct{})
	fns := []func(ctx context.Context) (int, error){
		// This goroutine holds the only semaphore slot.
		func(_ context.Context) (int, error) {
			close(acquired) // signal that the slot is held
			<-blocker
			return 1, nil
		},
		// This goroutine should not block forever on the semaphore;
		// it should observe context cancellation.
		func(_ context.Context) (int, error) {
			return 2, nil
		},
	}

	done := make(chan []Result[int], 1)
	go func() {
		done <- FanOutSettled(ctx, fns, WithMaxGoroutines(1))
	}()

	// Wait until the first goroutine has acquired the semaphore slot.
	<-acquired
	cancel()

	// Unblock the first goroutine so it can finish.
	close(blocker)

	select {
	case got := <-done:
		require.Len(t, got, 2)
		// The second function should have a context cancellation error
		// because it could not acquire the semaphore.
		assert.ErrorIs(t, got[1].Err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("FanOutSettled did not return; semaphore is not context-aware")
	}
}

// ---------------------------------------------------------------------------
// Multiple concurrent panics
// ---------------------------------------------------------------------------

func TestFanOut_MultipleConcurrentPanics(t *testing.T) {
	fns := []func(ctx context.Context) (int, error){
		func(_ context.Context) (int, error) { panic("panic-1") },
		func(_ context.Context) (int, error) { panic("panic-2") },
		func(_ context.Context) (int, error) { panic("panic-3") },
	}

	_, err := FanOut(context.Background(), fns)
	require.Error(t, err)

	var pe *PanicError
	require.ErrorAs(t, err, &pe)
	assert.NotEmpty(t, pe.Stack)
}

func TestFanOutSettled_MultipleConcurrentPanics(t *testing.T) {
	fns := []func(ctx context.Context) (int, error){
		func(_ context.Context) (int, error) { panic("panic-a") },
		func(_ context.Context) (int, error) { panic("panic-b") },
		func(_ context.Context) (int, error) { panic("panic-c") },
	}

	got := FanOutSettled(context.Background(), fns)
	require.Len(t, got, 3)

	for i, r := range got {
		require.Error(t, r.Err, "result %d should have an error", i)
		var pe *PanicError
		require.ErrorAs(t, r.Err, &pe, "result %d should be *PanicError", i)
		assert.NotEmpty(t, pe.Stack, "result %d should have a stack trace", i)
	}
}

// ---------------------------------------------------------------------------
// WithMaxGoroutines edge cases
// ---------------------------------------------------------------------------

func TestFanOut_WithMaxGoroutinesZero(t *testing.T) {
	fns := make([]func(ctx context.Context) (int, error), 5)
	for i := range fns {
		fns[i] = func(_ context.Context) (int, error) { return 1, nil }
	}

	got, err := FanOut(context.Background(), fns, WithMaxGoroutines(0))
	require.NoError(t, err)
	assert.Len(t, got, 5, "WithMaxGoroutines(0) should behave as unbounded")
}

func TestFanOut_WithMaxGoroutinesNegative(t *testing.T) {
	fns := make([]func(ctx context.Context) (int, error), 5)
	for i := range fns {
		fns[i] = func(_ context.Context) (int, error) { return 1, nil }
	}

	got, err := FanOut(context.Background(), fns, WithMaxGoroutines(-1))
	require.NoError(t, err)
	assert.Len(t, got, 5, "WithMaxGoroutines(-1) should behave as unbounded")
}

func TestFanOutSettled_WithMaxGoroutinesZero(t *testing.T) {
	fns := make([]func(ctx context.Context) (int, error), 5)
	for i := range fns {
		fns[i] = func(_ context.Context) (int, error) { return 1, nil }
	}

	got := FanOutSettled(context.Background(), fns, WithMaxGoroutines(0))
	assert.Len(t, got, 5, "WithMaxGoroutines(0) should behave as unbounded")
}

func TestFanOutSettled_WithMaxGoroutinesNegative(t *testing.T) {
	fns := make([]func(ctx context.Context) (int, error), 5)
	for i := range fns {
		fns[i] = func(_ context.Context) (int, error) { return 1, nil }
	}

	got := FanOutSettled(context.Background(), fns, WithMaxGoroutines(-1))
	assert.Len(t, got, 5, "WithMaxGoroutines(-1) should behave as unbounded")
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkFanOut(b *testing.B) {
	fns := make([]func(ctx context.Context) (int, error), 10)
	for i := range fns {
		fns[i] = func(_ context.Context) (int, error) { return 0, nil }
	}

	b.ResetTimer()
	for range b.N {
		_, _ = FanOut(context.Background(), fns)
	}
}

func BenchmarkFanOutSettled(b *testing.B) {
	fns := make([]func(ctx context.Context) (int, error), 10)
	for i := range fns {
		fns[i] = func(_ context.Context) (int, error) { return 0, nil }
	}

	b.ResetTimer()
	for range b.N {
		_ = FanOutSettled(context.Background(), fns)
	}
}
