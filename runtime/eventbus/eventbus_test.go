package eventbus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testEvent struct {
	ID string
}

func (testEvent) EventName() string { return "test.event" }

type otherEvent struct {
	Value int
}

func (otherEvent) EventName() string { return "other.event" }

func TestPublish_SyncHandler(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	var received testEvent
	Subscribe(bus, func(_ context.Context, e testEvent) error {
		received = e
		return nil
	})

	err := Publish(bus, context.Background(), testEvent{ID: "abc"})
	require.NoError(t, err)
	assert.Equal(t, "abc", received.ID)
}

func TestPublish_MultipleSyncHandlers(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	var order []int
	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		order = append(order, 1)
		return nil
	})
	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		order = append(order, 2)
		return nil
	})

	err := Publish(bus, context.Background(), testEvent{ID: "x"})
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, order)
}

func TestPublish_NoHandlers(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	err := Publish(bus, context.Background(), testEvent{ID: "x"})
	assert.NoError(t, err)
}

func TestPublish_SyncErrorCollection(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		return errors.New("first")
	}, WithName("h1"))
	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		return errors.New("second")
	}, WithName("h2"))

	err := Publish(bus, context.Background(), testEvent{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "first")
	assert.Contains(t, err.Error(), "second")
	assert.Contains(t, err.Error(), `handler "h1"`)
	assert.Contains(t, err.Error(), `handler "h2"`)
}

func TestPublish_AsyncHandler(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	var called atomic.Bool
	Subscribe(bus, func(_ context.Context, e testEvent) error {
		called.Store(true)
		return nil
	}, WithAsync())

	err := Publish(bus, context.Background(), testEvent{ID: "async"})
	require.NoError(t, err)

	assert.Eventually(t, called.Load, time.Second, 5*time.Millisecond)
}

func TestPublish_AsyncErrorCallsOnError(t *testing.T) {
	var (
		gotEvent   string
		gotHandler string
		gotErr     error
		done       = make(chan struct{})
	)

	bus := New(WithOnError(func(_ context.Context, eventName, handlerName string, err error) {
		gotEvent = eventName
		gotHandler = handlerName
		gotErr = err
		close(done)
	}))
	defer func() { _ = bus.Stop(context.Background()) }()

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		return errors.New("async failure")
	}, WithAsync(), WithName("failing"))

	err := Publish(bus, context.Background(), testEvent{})
	require.NoError(t, err) // async errors don't return from Publish

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("OnError not called")
	}

	assert.Equal(t, "test.event", gotEvent)
	assert.Equal(t, "failing", gotHandler)
	assert.Contains(t, gotErr.Error(), "async failure")
}

func TestPublish_AsyncPanicRecovery(t *testing.T) {
	var (
		gotErr error
		done   = make(chan struct{})
	)

	bus := New(WithOnError(func(_ context.Context, _, _ string, err error) {
		gotErr = err
		close(done)
	}))
	defer func() { _ = bus.Stop(context.Background()) }()

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		panic("boom")
	}, WithAsync(), WithName("panicker"))

	err := Publish(bus, context.Background(), testEvent{})
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("OnError not called after panic")
	}

	assert.Contains(t, gotErr.Error(), "boom")
}

func TestPublish_MixedSyncAsync(t *testing.T) {
	var (
		asyncCalled atomic.Bool
		asyncDone   = make(chan struct{})
	)

	bus := New(WithOnError(func(_ context.Context, _, _ string, _ error) {
		close(asyncDone)
	}))
	defer func() { _ = bus.Stop(context.Background()) }()

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		return errors.New("sync fail")
	}, WithName("sync-handler"))

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		asyncCalled.Store(true)
		return errors.New("async fail")
	}, WithAsync(), WithName("async-handler"))

	err := Publish(bus, context.Background(), testEvent{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync fail")
	assert.NotContains(t, err.Error(), "async fail")

	select {
	case <-asyncDone:
	case <-time.After(time.Second):
		t.Fatal("async handler not called")
	}

	assert.True(t, asyncCalled.Load())
}

func TestPublish_DifferentEventTypes(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	var testReceived bool
	var otherReceived bool

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		testReceived = true
		return nil
	})
	Subscribe(bus, func(_ context.Context, _ otherEvent) error {
		otherReceived = true
		return nil
	})

	err := Publish(bus, context.Background(), testEvent{ID: "x"})
	require.NoError(t, err)
	assert.True(t, testReceived)
	assert.False(t, otherReceived)

	err = Publish(bus, context.Background(), otherEvent{Value: 42})
	require.NoError(t, err)
	assert.True(t, otherReceived)
}

func TestSubscribe_PanicsOnNilHandler(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	assert.Panics(t, func() {
		Subscribe[testEvent](bus, nil)
	})
}

func TestSubscribe_WithName(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		return errors.New("oops")
	}, WithName("my-handler"))

	err := Publish(bus, context.Background(), testEvent{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `handler "my-handler"`)
}

func TestPublish_ContextPropagation(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "hello")

	// Sync handler: context passed directly.
	var syncGot string
	Subscribe(bus, func(ctx context.Context, _ testEvent) error {
		syncGot, _ = ctx.Value(ctxKey{}).(string)
		return nil
	})

	// Async handler: context is preserved because we use a single GoChannel
	// subscriber per event name (no message.Copy on single-subscriber fan-out).
	var asyncGot atomic.Value
	Subscribe(bus, func(ctx context.Context, _ testEvent) error {
		v, _ := ctx.Value(ctxKey{}).(string)
		asyncGot.Store(v)
		return nil
	}, WithAsync())

	err := Publish(bus, ctx, testEvent{})
	require.NoError(t, err)
	assert.Equal(t, "hello", syncGot)

	assert.Eventually(t, func() bool {
		v := asyncGot.Load()
		return v != nil && v.(string) == "hello"
	}, time.Second, 5*time.Millisecond)
}

func TestHasHandlers(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	assert.False(t, bus.HasHandlers("test.event"))

	Subscribe(bus, func(_ context.Context, _ testEvent) error { return nil })
	assert.True(t, bus.HasHandlers("test.event"))
	assert.False(t, bus.HasHandlers("other.event"))
}

func TestBus_ConcurrentPublishSubscribe(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	var count atomic.Int64

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Subscribe(bus, func(_ context.Context, _ testEvent) error {
				count.Add(1)
				return nil
			})
		}()
	}
	wg.Wait()

	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Publish(bus, context.Background(), testEvent{ID: "concurrent"})
		}()
	}
	wg.Wait()

	// Each publish calls all 10 sync handlers; 100 publishes = 1000 calls.
	assert.Equal(t, int64(1000), count.Load())
}

func TestWorkerPool_DropsWhenFull(t *testing.T) {
	reg := prometheus.NewRegistry()
	bus := New(
		WithWorkerPool(1),
		WithRegisterer(reg),
	)
	defer func() { _ = bus.Stop(context.Background()) }()

	blocker := make(chan struct{})
	var processed atomic.Int64

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		<-blocker
		processed.Add(1)
		return nil
	}, WithAsync(), WithName("blocker"))

	// First event occupies the single worker slot.
	_ = Publish(bus, context.Background(), testEvent{ID: "1"})

	// Wait for the worker to pick up the first event.
	time.Sleep(50 * time.Millisecond)

	// Additional events should be dropped (pool full).
	for range 5 {
		_ = Publish(bus, context.Background(), testEvent{ID: "overflow"})
	}

	// Wait for drops to be processed by the dispatcher.
	time.Sleep(50 * time.Millisecond)

	close(blocker)
	time.Sleep(50 * time.Millisecond)

	// At most 1 event processed (the first one that got the worker slot).
	// Some overflow events may have been processed if timing allowed.
	assert.LessOrEqual(t, processed.Load(), int64(2))
}

func TestWorkerPool_ProcessesEvents(t *testing.T) {
	reg := prometheus.NewRegistry()
	bus := New(
		WithWorkerPool(20), // large enough to not drop any of the 20 events
		WithRegisterer(reg),
	)
	defer func() { _ = bus.Stop(context.Background()) }()

	const eventCount = 20
	var processed atomic.Int64

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		processed.Add(1)
		return nil
	}, WithAsync(), WithName("counter"))

	for range eventCount {
		_ = Publish(bus, context.Background(), testEvent{ID: "x"})
	}

	assert.Eventually(t, func() bool {
		return processed.Load() == eventCount
	}, 2*time.Second, 10*time.Millisecond)
}

func TestBus_StartStopLifecycle(t *testing.T) {
	bus := New()

	ctx, cancel := context.WithCancel(context.Background())
	var startErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		startErr = bus.Start(ctx)
	}()

	cancel()
	wg.Wait()

	assert.NoError(t, startErr)
	assert.NoError(t, bus.Stop(context.Background()))
}

func TestBus_DoubleStopDoesNotPanic(t *testing.T) {
	bus := New()
	assert.NotPanics(t, func() {
		_ = bus.Stop(context.Background())
		_ = bus.Stop(context.Background())
	})
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

func BenchmarkPublish_Sync(b *testing.B) {
	bus := New()
	defer func() { _ = bus.Stop(context.Background()) }()

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		return nil
	}, WithName("noop"))

	ctx := context.Background()
	evt := testEvent{ID: "bench"}

	b.ResetTimer()
	for range b.N {
		_ = Publish(bus, ctx, evt)
	}
}

func BenchmarkPublish_Async(b *testing.B) {
	reg := prometheus.NewRegistry()
	bus := New(
		WithWorkerPool(4),
		WithRegisterer(reg),
	)
	defer func() { _ = bus.Stop(context.Background()) }()

	Subscribe(bus, func(_ context.Context, _ testEvent) error {
		return nil
	}, WithAsync(), WithName("noop"))

	// Warmup.
	_ = Publish(bus, context.Background(), testEvent{ID: "warmup"})
	time.Sleep(50 * time.Millisecond)

	ctx := context.Background()
	evt := testEvent{ID: "bench"}

	b.ResetTimer()
	for range b.N {
		_ = Publish(bus, ctx, evt)
	}
}
