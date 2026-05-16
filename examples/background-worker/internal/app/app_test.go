package app

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// TestTypedSubscription_DispatchesValidatedEvents drives the
// canonical happy path: inject two valid events, observe that the
// processor's record matches what was sent.
func TestTypedSubscription_DispatchesValidatedEvents(t *testing.T) {
	logger := slog.Default()
	consumer := newFakeConsumer()
	processor := newOrderProcessor(logger)
	resilient := wrapWithResilience(processor.handle, logger)
	typed := func(ctx context.Context, ev OrderEvent, _ messaging.Delivery) error {
		return resilient(ctx, ev)
	}

	sub := messaging.NewTypedSubscription[OrderEvent](
		"test-orders",
		consumer,
		messaging.Binding{
			BindingSpec: messaging.BindingSpec{
				Exchange:      "orders",
				ConsumerGroup: "billing",
				RoutingKey:    "order.created",
				WithoutRetry:  true,
			},
		},
		typed,
		messaging.WithSubscriptionLogger(logger),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sub.Start(ctx)
	}()

	// Give Consume time to install the handler.
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, consumer.Inject(OrderEvent{OrderID: "ord-1", Amount: 10}))
	require.NoError(t, consumer.Inject(OrderEvent{OrderID: "ord-2", Amount: 20}))

	// Poll until the processor has both events or the deadline fires.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if len(processor.snapshot()) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	processed := processor.snapshot()
	require.Len(t, processed, 2, "both injected events must be dispatched")
	assert.Equal(t, "ord-1", processed[0].OrderID)
	assert.Equal(t, "ord-2", processed[1].OrderID)

	cancel()
	wg.Wait()
}

// TestResilientWrapper_RetriesTransientFailure asserts that the
// retry policy gives the handler a second chance before bubbling
// the error.
func TestResilientWrapper_RetriesTransientFailure(t *testing.T) {
	var attempts atomic.Int32
	inner := func(_ context.Context, _ OrderEvent) error {
		if attempts.Add(1) == 1 {
			return assert.AnError
		}
		return nil
	}
	wrapped := wrapWithResilience(inner, slog.Default())
	err := wrapped(context.Background(), OrderEvent{OrderID: "x", Amount: 1})
	require.NoError(t, err)
	assert.Equal(t, int32(2), attempts.Load(), "retry must give the handler a second attempt")
}

// TestFakeConsumer_RespectsCtxCancellation verifies the in-process
// consumer returns cleanly when ctx is cancelled.
func TestFakeConsumer_RespectsCtxCancellation(t *testing.T) {
	c := newFakeConsumer()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.Consume(ctx, messaging.Binding{}, func(context.Context, messaging.Delivery) error { return nil })
	}()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Consume did not return after ctx cancel")
	}
}
