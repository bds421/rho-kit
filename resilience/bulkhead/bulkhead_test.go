package bulkhead

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_PanicsOnNonPositiveMax(t *testing.T) {
	assert.Panics(t, func() { New("ok", 0) })
	assert.Panics(t, func() { New("ok", -1) })
}

func TestNew_PanicsOnHighCardinalityName(t *testing.T) {
	// promutil.ValidateStaticLabelValue rejects values containing
	// unsafe characters; this catches a misuse before it inflates
	// Prometheus cardinality.
	assert.Panics(t, func() {
		New("tenant-"+string([]byte{0x01}), 5)
	})
}

func TestExecuteCtx_HappyPath(t *testing.T) {
	b := New("test", 2)
	var called int32
	err := b.ExecuteCtx(context.Background(), func(context.Context) error {
		atomic.AddInt32(&called, 1)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), called)
	assert.Equal(t, 0, b.InFlight(), "slot must be released after fn returns")
}

func TestExecuteCtx_PropagatesFnError(t *testing.T) {
	b := New("test", 2)
	sentinel := errors.New("downstream boom")
	err := b.ExecuteCtx(context.Background(), func(context.Context) error {
		return sentinel
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "redact.WrapError preserves the wrapped chain")
	assert.Equal(t, 0, b.InFlight())
}

func TestExecuteCtx_FullImmediateRejection(t *testing.T) {
	b := New("test", 1)
	hold := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = b.ExecuteCtx(context.Background(), func(context.Context) error {
			<-hold
			return nil
		})
	}()
	// Wait until the goroutine has taken the slot.
	for b.InFlight() == 0 {
		time.Sleep(time.Millisecond)
	}

	// MaxQueueWait defaults to <= 0 (no wait); a full bulkhead
	// rejects immediately.
	err := b.ExecuteCtx(context.Background(), func(context.Context) error {
		t.Fatal("must not run when bulkhead is full")
		return nil
	})
	assert.ErrorIs(t, err, ErrBulkheadFull)
	close(hold)
	<-done
}

func TestExecuteCtx_WaitsWhenMaxQueueWaitSet(t *testing.T) {
	b := New("test", 1, WithMaxQueueWait(200*time.Millisecond))
	hold := make(chan struct{})
	go func() {
		_ = b.ExecuteCtx(context.Background(), func(context.Context) error {
			<-hold
			return nil
		})
	}()
	for b.InFlight() == 0 {
		time.Sleep(time.Millisecond)
	}

	// Release the first holder ~50ms after the second one starts
	// waiting. The second one should acquire and succeed (well
	// within the 200ms wait budget).
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(hold)
	}()

	var ran int32
	err := b.ExecuteCtx(context.Background(), func(context.Context) error {
		atomic.AddInt32(&ran, 1)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), ran)
}

func TestExecuteCtx_TimeoutAfterWait(t *testing.T) {
	b := New("test", 1, WithMaxQueueWait(20*time.Millisecond))
	hold := make(chan struct{})
	defer close(hold)
	go func() {
		_ = b.ExecuteCtx(context.Background(), func(context.Context) error {
			<-hold
			return nil
		})
	}()
	for b.InFlight() == 0 {
		time.Sleep(time.Millisecond)
	}

	err := b.ExecuteCtx(context.Background(), func(context.Context) error {
		t.Fatal("must not run after timeout")
		return nil
	})
	assert.ErrorIs(t, err, ErrBulkheadFull, "exceeding MaxQueueWait must return ErrBulkheadFull")
}

func TestExecuteCtx_RespectsCallerCtxCancel(t *testing.T) {
	b := New("test", 1, WithMaxQueueWait(1*time.Hour))
	hold := make(chan struct{})
	defer close(hold)
	go func() {
		_ = b.ExecuteCtx(context.Background(), func(context.Context) error {
			<-hold
			return nil
		})
	}()
	for b.InFlight() == 0 {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := b.ExecuteCtx(ctx, func(context.Context) error {
		t.Fatal("must not run after ctx cancel")
		return nil
	})
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled),
		"caller ctx cancel must abort the wait")
}

func TestExecuteCtx_ReleasesSlotOnPanic(t *testing.T) {
	b := New("test", 1)
	defer func() {
		_ = recover()
		// Slot must be released even though fn panicked.
		assert.Equal(t, 0, b.InFlight(), "panicking fn must still release the slot")
	}()
	_ = b.ExecuteCtx(context.Background(), func(context.Context) error {
		panic("downstream blew up")
	})
}

func TestExecuteCtx_NilCtxAndNilFnRejected(t *testing.T) {
	b := New("test", 1)
	assert.Error(t, b.ExecuteCtx(nil, func(context.Context) error { return nil })) //nolint:staticcheck // intentional nil-ctx contract test
	assert.Error(t, b.ExecuteCtx(context.Background(), nil))
}

// TestBulkhead_ConcurrentLoad exercises the semaphore under
// pressure to make sure InFlight() never exceeds Capacity().
func TestBulkhead_ConcurrentLoad(t *testing.T) {
	const cap = 4
	b := New("test", cap, WithMaxQueueWait(1*time.Second))

	var maxObserved int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.ExecuteCtx(context.Background(), func(context.Context) error {
				if v := int32(b.InFlight()); v > atomic.LoadInt32(&maxObserved) {
					atomic.StoreInt32(&maxObserved, v)
				}
				time.Sleep(2 * time.Millisecond)
				return nil
			})
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, int(atomic.LoadInt32(&maxObserved)), cap,
		"in-flight must never exceed capacity under contention")
}

func TestExecuteCtx_PreCancelledCtxDoesNotAcquire(t *testing.T) {
	b := New("pre-cancel", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := b.ExecuteCtx(ctx, func(context.Context) error {
		called = true
		return nil
	})
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, called, "fn must not run when ctx is already cancelled")
	assert.Equal(t, 0, b.InFlight())
}
