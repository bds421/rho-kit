package messaging

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestSubscriptionStop_WaitsForLateCancel is a white-box test for the
// Start/Stop race window: Start sets the started flag (CompareAndSwap)
// before it publishes the cancel func. A Stop that races into that
// window must NOT swap a nil cancel, cancel nothing, and then block on
// done until its own ctx expires while the consumer keeps running on the
// un-cancelled runCtx. Instead Stop must wait for the cancel func to be
// published and then invoke it.
func TestSubscriptionStop_WaitsForLateCancel(t *testing.T) {
	sub := NewSubscription("race", &raceConsumer{}, Binding{},
		func(context.Context, Delivery) error { return nil },
		WithSubscriptionLogger(discardLogger()),
	)

	// Simulate the window: started is set, but cancel is not yet
	// published (Start has done its CAS but not yet Store).
	sub.started.Store(true)

	var cancelled atomic.Bool
	var cancel context.CancelFunc = func() { cancelled.Store(true) }

	ctx, cancelCtx := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelCtx()

	stopReturned := make(chan error, 1)
	go func() { stopReturned <- sub.Stop(ctx) }()

	// Let Stop spin in the await window briefly, then publish the cancel
	// exactly as Start would, followed by closing done as Start's defer
	// would when the consumer returns after cancellation.
	time.Sleep(50 * time.Millisecond)
	if cancelled.Load() {
		t.Fatal("cancel invoked before it was ever published")
	}
	sub.cancel.Store(&cancel)

	// Wait for Stop to take and invoke the now-published cancel.
	deadline := time.Now().Add(time.Second)
	for !cancelled.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Stop did not invoke the late-published cancel func")
		}
		time.Sleep(time.Millisecond)
	}

	// Now close done so Stop's final wait returns nil.
	close(sub.done)

	select {
	case err := <-stopReturned:
		if err != nil {
			t.Fatalf("Stop returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after done closed")
	}
}

// TestSubscriptionStop_LateCancelHonoursContext verifies that when Stop
// races the window but ctx expires before Start ever publishes a cancel,
// Stop returns the context error instead of spinning forever.
func TestSubscriptionStop_LateCancelHonoursContext(t *testing.T) {
	sub := NewSubscription("race-ctx", &raceConsumer{}, Binding{},
		func(context.Context, Delivery) error { return nil },
		WithSubscriptionLogger(discardLogger()),
	)
	sub.started.Store(true) // window: started set, cancel never published

	ctx, cancelCtx := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelCtx()

	done := make(chan error, 1)
	go func() { done <- sub.Stop(ctx) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ctx error when cancel never publishes, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop spun forever instead of honouring ctx deadline")
	}
}

type raceConsumer struct{}

func (raceConsumer) Consume(ctx context.Context, _ Binding, _ Handler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (r raceConsumer) ConsumeOnce(ctx context.Context, b Binding, h Handler) error {
	return r.Consume(ctx, b, h)
}
