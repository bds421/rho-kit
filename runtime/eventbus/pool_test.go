package eventbus

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitForWorkers publishes a canary event on the bus and blocks until a worker
// processes it, proving the pool is fully running. This replaces fragile
// time.Sleep-based worker startup waits.
func waitForWorkers(t *testing.T, bus *Bus) {
	t.Helper()
	done := make(chan struct{})
	Subscribe(bus, func(_ context.Context, _ otherEvent) error {
		close(done)
		return nil
	}, WithAsync(), WithName("warmup-canary"))
	err := Publish(bus, context.Background(), otherEvent{Value: 0})
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForWorkers: canary event was not processed within 2s")
	}
}

func TestWorkerPool_ProcessesAllEvents(t *testing.T) {
	reg := prometheus.NewRegistry()
	bus := New(
		WithWorkerPool(4),
		WithWorkerPoolBuffer(100),
		WithRegisterer(reg),
	)

	const eventCount = 50
	var processed atomic.Int64

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		processed.Add(1)
		return nil
	}, WithAsync(), WithName("counter"))

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_ = bus.Start(ctx)
	}()
	<-started
	waitForWorkers(t, bus)

	for i := range eventCount {
		err := Publish(bus, context.Background(), testEvent{ID: string(rune('a' + i%26))})
		require.NoError(t, err)
	}

	assert.Eventually(t, func() bool {
		return processed.Load() == eventCount
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	err := bus.Stop(context.Background())
	require.NoError(t, err)
}

func TestWorkerPool_DropsWhenQueueFull(t *testing.T) {
	reg := prometheus.NewRegistry()

	// 1 worker, buffer of 1 — easy to overflow.
	bus := New(
		WithWorkerPool(1),
		WithWorkerPoolBuffer(1),
		WithRegisterer(reg),
	)

	blocker := make(chan struct{})
	var processed atomic.Int64

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		<-blocker // block until released
		processed.Add(1)
		return nil
	}, WithAsync(), WithName("blocker"))

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_ = bus.Start(ctx)
	}()
	<-started
	waitForWorkers(t, bus)

	// First event fills the worker, second fills the buffer, third+ should drop.
	for range 10 {
		_ = Publish(bus, context.Background(), testEvent{ID: "overflow"})
	}

	// Unblock the worker and stop.
	close(blocker)
	cancel()
	err := bus.Stop(context.Background())
	require.NoError(t, err)

	// Some events must have been dropped (processed < 10).
	assert.Less(t, processed.Load(), int64(10))
}

func TestWorkerPool_StopDrainsPendingEvents(t *testing.T) {
	reg := prometheus.NewRegistry()
	bus := New(
		WithWorkerPool(2),
		WithWorkerPoolBuffer(50),
		WithRegisterer(reg),
	)

	var processed atomic.Int64

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		time.Sleep(time.Millisecond) // simulate work
		processed.Add(1)
		return nil
	}, WithAsync(), WithName("slow"))

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_ = bus.Start(ctx)
	}()
	<-started
	waitForWorkers(t, bus)

	const eventCount = 20
	for range eventCount {
		_ = Publish(bus, context.Background(), testEvent{ID: "drain"})
	}

	// Cancel context to stop accepting new tasks, then stop drains remaining.
	cancel()
	err := bus.Stop(context.Background())
	require.NoError(t, err)

	// All submitted events should have been processed during drain.
	assert.Equal(t, int64(eventCount), processed.Load())
}

func TestWorkerPool_PanicDoesNotCrashPool(t *testing.T) {
	reg := prometheus.NewRegistry()
	var (
		panicErr error
		errDone  = make(chan struct{}, 10)
	)

	bus := New(
		WithWorkerPool(2),
		WithWorkerPoolBuffer(20),
		WithRegisterer(reg),
		WithOnError(func(_ context.Context, _, _ string, err error) {
			panicErr = err
			errDone <- struct{}{}
		}),
	)

	var processed atomic.Int64

	// Register a panicking handler.
	Subscribe(bus, func(_ context.Context, e testEvent) error {
		if e.ID == "panic" {
			panic("boom")
		}
		processed.Add(1)
		return nil
	}, WithAsync(), WithName("maybe-panic"))

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_ = bus.Start(ctx)
	}()
	<-started
	waitForWorkers(t, bus)

	// Send a panic event, then a normal event.
	_ = Publish(bus, context.Background(), testEvent{ID: "panic"})

	select {
	case <-errDone:
	case <-time.After(time.Second):
		t.Fatal("OnError not called after panic")
	}
	assert.Contains(t, panicErr.Error(), "boom")

	// Pool should still be alive for subsequent events.
	_ = Publish(bus, context.Background(), testEvent{ID: "ok"})
	assert.Eventually(t, func() bool {
		return processed.Load() == 1
	}, time.Second, 5*time.Millisecond)

	cancel()
	_ = bus.Stop(context.Background())
}

func TestWithoutWorkerPool_LegacyBehaviorPreserved(t *testing.T) {
	bus := New()
	var called atomic.Bool

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		called.Store(true)
		return nil
	}, WithAsync())

	err := Publish(bus, context.Background(), testEvent{ID: "legacy"})
	require.NoError(t, err)

	assert.Eventually(t, called.Load, 100*time.Millisecond, 5*time.Millisecond)

	// Start/Stop are no-ops without pool config.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = bus.Start(ctx)
		close(done)
	}()
	cancel()
	<-done
	assert.NoError(t, bus.Stop(context.Background()))
}

func TestWorkerPool_BoundedGoroutines(t *testing.T) {
	reg := prometheus.NewRegistry()
	bus := New(
		WithWorkerPool(4),
		WithWorkerPoolBuffer(100),
		WithRegisterer(reg),
	)

	blocker := make(chan struct{})
	var waiting atomic.Int64

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		waiting.Add(1)
		<-blocker
		return nil
	}, WithAsync(), WithName("block"))

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_ = bus.Start(ctx)
	}()
	<-started
	waitForWorkers(t, bus)

	baseGoroutines := runtime.NumGoroutine()

	// Publish many events — goroutine count should remain bounded.
	for range 50 {
		_ = Publish(bus, context.Background(), testEvent{ID: "bounded"})
	}

	// Wait until all 4 workers are blocked in handlers before checking goroutine count.
	require.Eventually(t, func() bool {
		return waiting.Load() == 4
	}, 2*time.Second, 5*time.Millisecond, "workers should all be blocked")

	currentGoroutines := runtime.NumGoroutine()
	// With a pool of 4, goroutine growth should be minimal.
	// Allow some slack for test infra goroutines.
	assert.Less(t, currentGoroutines-baseGoroutines, 10,
		"goroutine count should remain bounded with worker pool")

	close(blocker)
	cancel()
	_ = bus.Stop(context.Background())
}

func TestBus_StartStopLifecycle(t *testing.T) {
	reg := prometheus.NewRegistry()
	bus := New(
		WithWorkerPool(2),
		WithWorkerPoolBuffer(10),
		WithRegisterer(reg),
	)

	ctx, cancel := context.WithCancel(context.Background())
	var startErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		startErr = bus.Start(ctx)
	}()

	waitForWorkers(t, bus)
	assert.NotNil(t, bus.pool, "pool should be initialized")

	cancel()
	stopErr := bus.Stop(context.Background())
	wg.Wait()

	assert.NoError(t, startErr)
	assert.NoError(t, stopErr)
}

func TestWithWorkerPool_PanicsOnZeroSize(t *testing.T) {
	assert.Panics(t, func() {
		New(WithWorkerPool(0))
	})
}

func TestWithWorkerPool_PanicsOnNegativeSize(t *testing.T) {
	assert.Panics(t, func() {
		New(WithWorkerPool(-1))
	})
}

func TestWithWorkerPoolBuffer_PanicsOnZeroSize(t *testing.T) {
	assert.Panics(t, func() {
		New(WithWorkerPoolBuffer(0))
	})
}

func TestWithWorkerPoolBuffer_WithoutWorkerPool_NoPool(t *testing.T) {
	bus := New(WithWorkerPoolBuffer(50))
	assert.Nil(t, bus.pool, "pool should be nil when WithWorkerPool is not used")
}

func TestWorkerPool_AsyncErrorCallsOnError(t *testing.T) {
	reg := prometheus.NewRegistry()
	var (
		gotEvent   string
		gotHandler string
		gotErr     error
		done       = make(chan struct{})
	)

	bus := New(
		WithWorkerPool(2),
		WithWorkerPoolBuffer(10),
		WithRegisterer(reg),
		WithOnError(func(_ context.Context, eventName, handlerName string, err error) {
			gotEvent = eventName
			gotHandler = handlerName
			gotErr = err
			close(done)
		}),
	)

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		return errors.New("pool async failure")
	}, WithAsync(), WithName("failing"))

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_ = bus.Start(ctx)
	}()
	<-started
	waitForWorkers(t, bus)

	err := Publish(bus, context.Background(), testEvent{})
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("OnError not called")
	}

	assert.Equal(t, "test.event", gotEvent)
	assert.Equal(t, "failing", gotHandler)
	assert.Contains(t, gotErr.Error(), "pool async failure")

	cancel()
	_ = bus.Stop(context.Background())
}

func TestWorkerPool_SubmitAfterStopDoesNotPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	bus := New(
		WithWorkerPool(2),
		WithWorkerPoolBuffer(10),
		WithRegisterer(reg),
	)

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		return nil
	}, WithAsync(), WithName("noop"))

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_ = bus.Start(ctx)
	}()
	<-started
	waitForWorkers(t, bus)

	cancel()
	err := bus.Stop(context.Background())
	require.NoError(t, err)

	// Submit after stop must not panic; it should return false (drop).
	assert.NotPanics(t, func() {
		ok := bus.pool.submit(&asyncTask{
			ctx:       context.Background(),
			eventName: "test.event",
			handler:   registeredHandler{name: "late"},
			event:     testEvent{ID: "late"},
		})
		assert.False(t, ok)
	})
}

func TestWorkerPool_DoubleStopDoesNotPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	bus := New(
		WithWorkerPool(2),
		WithWorkerPoolBuffer(10),
		WithRegisterer(reg),
	)

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_ = bus.Start(ctx)
	}()
	<-started
	waitForWorkers(t, bus)

	cancel()

	assert.NotPanics(t, func() {
		_ = bus.Stop(context.Background())
		_ = bus.Stop(context.Background())
	})
}

func TestBus_StopRespectsContextDeadline(t *testing.T) {
	reg := prometheus.NewRegistry()
	bus := New(
		WithWorkerPool(1),
		WithWorkerPoolBuffer(10),
		WithRegisterer(reg),
	)

	blocker := make(chan struct{})
	running := make(chan struct{})
	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		close(running) // signal we're in the handler
		<-blocker      // block forever
		return nil
	}, WithAsync(), WithName("blocker"))

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_ = bus.Start(ctx)
	}()
	<-started
	waitForWorkers(t, bus)

	// Submit an event that will block the worker.
	_ = Publish(bus, context.Background(), testEvent{ID: "block"})
	<-running

	// Cancel the start context so no new tasks are accepted.
	cancel()

	// Stop with a very short deadline — the worker is blocked, so it should time out.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stopCancel()
	err := bus.Stop(stopCtx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// Unblock the worker to prevent goroutine leak.
	close(blocker)
}

func TestWorkerPool_SubmitBeforeStartLogsWarning(t *testing.T) {
	reg := prometheus.NewRegistry()
	bus := New(
		WithWorkerPool(2),
		WithWorkerPoolBuffer(10),
		WithRegisterer(reg),
	)

	// Submit before start must not panic; event may be buffered.
	assert.NotPanics(t, func() {
		ok := bus.pool.submit(&asyncTask{
			ctx:       context.Background(),
			eventName: "test.event",
			handler:   registeredHandler{name: "early"},
			event:     testEvent{ID: "early"},
		})
		// The event should be buffered in the channel since pool is not stopped.
		assert.True(t, ok)
	})

	// Start and drain.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = bus.Start(ctx) }()
	waitForWorkers(t, bus)
	cancel()
	_ = bus.Stop(context.Background())
}
