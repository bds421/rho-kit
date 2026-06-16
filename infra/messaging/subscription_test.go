package messaging_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func newQuietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeConsumer satisfies [messaging.Consumer] for unit tests. Each
// instance receives a slice of Deliveries to dispatch when Consume
// is called; once exhausted it blocks on ctx.Done.
type fakeConsumer struct {
	deliveries  []messaging.Delivery
	consumeErr  error
	handlerErrs []error
	mu          sync.Mutex
	dispatched  []messaging.Delivery
	called      atomic.Int32
}

func (f *fakeConsumer) Consume(ctx context.Context, _ messaging.Binding, h messaging.Handler) error {
	f.called.Add(1)
	for i, d := range f.deliveries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		f.mu.Lock()
		f.dispatched = append(f.dispatched, d)
		f.mu.Unlock()
		var wantErr error
		if i < len(f.handlerErrs) {
			wantErr = f.handlerErrs[i]
		}
		if err := h(ctx, d); err != nil && wantErr == nil {
			return err
		}
	}
	if f.consumeErr != nil {
		return f.consumeErr
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeConsumer) ConsumeOnce(ctx context.Context, b messaging.Binding, h messaging.Handler) error {
	return f.Consume(ctx, b, h)
}

func TestNewSubscription_PanicsOnInvalidInput(t *testing.T) {
	cons := &fakeConsumer{}
	bind := messaging.Binding{}
	h := messaging.Handler(func(context.Context, messaging.Delivery) error { return nil })

	cases := []struct {
		name string
		fn   func()
	}{
		{"empty-name", func() { messaging.NewSubscription("", cons, bind, h) }},
		{"nil-consumer", func() { messaging.NewSubscription("n", nil, bind, h) }},
		{"nil-handler", func() { messaging.NewSubscription("n", cons, bind, nil) }},
		{"nil-option", func() { messaging.NewSubscription("n", cons, bind, h, nil) }},
		{"nil-logger-option", func() { messaging.WithSubscriptionLogger(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected panic in %s", tc.name)
				}
			}()
			tc.fn()
		})
	}
}

func TestSubscription_NameRoundTrip(t *testing.T) {
	sub := messaging.NewSubscription("orders-consumer",
		&fakeConsumer{},
		messaging.Binding{},
		func(context.Context, messaging.Delivery) error { return nil },
	)
	assert.Equal(t, "orders-consumer", sub.Name())
}

func TestSubscription_DispatchesDeliveries(t *testing.T) {
	deliveries := []messaging.Delivery{
		{Message: messaging.Message{ID: "1"}},
		{Message: messaging.Message{ID: "2"}},
	}
	cons := &fakeConsumer{deliveries: deliveries}

	var observed []string
	h := func(_ context.Context, d messaging.Delivery) error {
		observed = append(observed, d.Message.ID)
		return nil
	}

	sub := messaging.NewSubscription("test", cons, messaging.Binding{}, h,
		messaging.WithSubscriptionLogger(newQuietLogger()),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- sub.Start(ctx) }()

	require.Eventually(t, func() bool {
		cons.mu.Lock()
		defer cons.mu.Unlock()
		return len(cons.dispatched) == len(deliveries)
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)
	assert.Equal(t, []string{"1", "2"}, observed)
}

func TestSubscription_DoubleStartRejected(t *testing.T) {
	sub := messaging.NewSubscription("test",
		&fakeConsumer{},
		messaging.Binding{},
		func(context.Context, messaging.Delivery) error { return nil },
		messaging.WithSubscriptionLogger(newQuietLogger()),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = sub.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	err := sub.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already invoked")
}

func TestSubscription_StopBeforeStartIsNoOp(t *testing.T) {
	sub := messaging.NewSubscription("test",
		&fakeConsumer{},
		messaging.Binding{},
		func(context.Context, messaging.Delivery) error { return nil },
		messaging.WithSubscriptionLogger(newQuietLogger()),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, sub.Stop(ctx))
}

// TestSubscription_SurfacesConsumerError verifies that
// non-cancellation errors from the consumer bubble out as the
// Subscription's exit error so lifecycle.Runner can act on them.
func TestSubscription_SurfacesConsumerError(t *testing.T) {
	wantErr := errors.New("simulated broker disconnect")
	cons := &fakeConsumer{consumeErr: wantErr}

	sub := messaging.NewSubscription("test", cons, messaging.Binding{},
		func(context.Context, messaging.Delivery) error { return nil },
		messaging.WithSubscriptionLogger(newQuietLogger()),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := sub.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "consume")
}

type orderEvent struct {
	ID    string `json:"id" jsonschema:"required"`
	Items int    `json:"items" jsonschema:"required,min=1"`
}

func TestTypedSubscription_DispatchesDecodedPayload(t *testing.T) {
	payload, _ := json.Marshal(orderEvent{ID: "o-1", Items: 3})
	cons := &fakeConsumer{
		deliveries: []messaging.Delivery{
			{Message: messaging.Message{Payload: payload}},
		},
	}

	var got atomic.Pointer[orderEvent]
	h := func(_ context.Context, msg orderEvent, _ messaging.Delivery) error {
		clone := msg
		got.Store(&clone)
		return nil
	}

	sub := messaging.NewTypedSubscription[orderEvent]("typed", cons, messaging.Binding{}, h,
		messaging.WithSubscriptionLogger(newQuietLogger()),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sub.Start(ctx) }()

	require.Eventually(t, func() bool { return got.Load() != nil },
		2*time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)

	final := got.Load()
	require.NotNil(t, final)
	assert.Equal(t, "o-1", final.ID)
	assert.Equal(t, 3, final.Items)
}

func TestTypedSubscription_DecodeFailureSurfaces(t *testing.T) {
	cons := &fakeConsumer{
		deliveries: []messaging.Delivery{
			{Message: messaging.Message{Payload: []byte("not json")}},
		},
		handlerErrs: []error{errors.New("dummy")}, // tell fake to ignore handler err so Consume completes
	}

	called := false
	h := func(_ context.Context, _ orderEvent, _ messaging.Delivery) error {
		called = true
		return nil
	}

	sub := messaging.NewTypedSubscription[orderEvent]("typed-bad", cons, messaging.Binding{}, h,
		messaging.WithSubscriptionLogger(newQuietLogger()),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sub.Start(ctx) }()

	// Wait briefly for the bad delivery to be processed.
	time.Sleep(150 * time.Millisecond)
	cancel()

	assert.False(t, called,
		"typed handler must NOT be called for un-decodable payloads — kit returns the decode error to the consumer for nack/dead-letter")
}

func TestTypedSubscription_ValidationFailsForInvalidPayload(t *testing.T) {
	// Items=0 violates the jsonschema:"min=1" tag.
	payload, _ := json.Marshal(orderEvent{ID: "o-2", Items: 0})
	cons := &fakeConsumer{
		deliveries:  []messaging.Delivery{{Message: messaging.Message{Payload: payload}}},
		handlerErrs: []error{errors.New("dummy")},
	}

	called := false
	h := func(_ context.Context, _ orderEvent, _ messaging.Delivery) error {
		called = true
		return nil
	}

	sub := messaging.NewTypedSubscription[orderEvent]("typed-validate", cons, messaging.Binding{}, h,
		messaging.WithSubscriptionLogger(newQuietLogger()),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sub.Start(ctx) }()
	time.Sleep(150 * time.Millisecond)
	cancel()

	assert.False(t, called,
		"validation failure must prevent handler dispatch")
}

func TestTypedSubscription_WithoutValidation_BypassesSchemaCheck(t *testing.T) {
	payload, _ := json.Marshal(orderEvent{ID: "o-3", Items: 0}) // would fail validation
	cons := &fakeConsumer{
		deliveries: []messaging.Delivery{{Message: messaging.Message{Payload: payload}}},
	}

	called := atomic.Bool{}
	h := func(_ context.Context, msg orderEvent, _ messaging.Delivery) error {
		called.Store(true)
		assert.Equal(t, "o-3", msg.ID)
		return nil
	}

	sub := messaging.NewTypedSubscription[orderEvent]("typed-novalidate", cons, messaging.Binding{}, h,
		messaging.WithSubscriptionLogger(newQuietLogger()),
		messaging.WithoutTypedValidation(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sub.Start(ctx) }()

	require.Eventually(t, called.Load, 2*time.Second, 10*time.Millisecond,
		"handler must be called when validation is suppressed")
	cancel()
}

func TestSubscriptionGroup_StartsAllSubsConcurrently(t *testing.T) {
	cons1 := &fakeConsumer{}
	cons2 := &fakeConsumer{}
	sub1 := messaging.NewSubscription("a", cons1, messaging.Binding{},
		func(context.Context, messaging.Delivery) error { return nil },
		messaging.WithSubscriptionLogger(newQuietLogger()),
	)
	sub2 := messaging.NewSubscription("b", cons2, messaging.Binding{},
		func(context.Context, messaging.Delivery) error { return nil },
		messaging.WithSubscriptionLogger(newQuietLogger()),
	)

	g := messaging.NewSubscriptionGroup(newQuietLogger())
	require.NoError(t, g.Add(sub1))
	require.NoError(t, g.Add(sub2))
	assert.Equal(t, 2, g.Len())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- g.Start(ctx) }()

	require.Eventually(t, func() bool {
		return cons1.called.Load() >= 1 && cons2.called.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond,
		"both consumers must enter Consume in parallel")

	cancel()
	<-done
}

func TestSubscriptionGroup_AddAfterStartRejected(t *testing.T) {
	g := messaging.NewSubscriptionGroup(newQuietLogger())
	sub := messaging.NewSubscription("a",
		&fakeConsumer{},
		messaging.Binding{},
		func(context.Context, messaging.Delivery) error { return nil },
		messaging.WithSubscriptionLogger(newQuietLogger()),
	)
	require.NoError(t, g.Add(sub))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = g.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	err := g.Add(sub)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Add after Start")
}

func TestSubscriptionGroup_EmptyIsNoOp(t *testing.T) {
	g := messaging.NewSubscriptionGroup(newQuietLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := g.Start(ctx)
	// Empty group returns when ctx times out — context.DeadlineExceeded.
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestSubscriptionGroup_StopCancelsEmptyRunningGroup verifies that Stop
// can cancel an empty group that is already running on a never-cancelled
// parent context. The empty-group Start path now publishes the group's
// cancel func before parking, so Stop has something to cancel.
func TestSubscriptionGroup_StopCancelsEmptyRunningGroup(t *testing.T) {
	g := messaging.NewSubscriptionGroup(newQuietLogger())

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	startDone := make(chan error, 1)
	go func() { startDone <- g.Start(runCtx) }()

	// Give Start a moment to enter the parked state.
	time.Sleep(50 * time.Millisecond)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	require.NoError(t, g.Stop(stopCtx))

	select {
	case <-startDone:
		// Start returned because Stop cancelled the group.
	case <-time.After(2 * time.Second):
		t.Fatal("empty group Start did not return after Stop")
	}
}

func TestSubscriptionGroup_AddNilPanics(t *testing.T) {
	g := messaging.NewSubscriptionGroup(newQuietLogger())
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil Subscription")
		}
	}()
	_ = g.Add(nil)
}

func TestSubscriptionGroup_StopBeforeStartIsNoOp(t *testing.T) {
	g := messaging.NewSubscriptionGroup(newQuietLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Stop(ctx))
}

// TestSubscriptionGroup_StopBeforeStartDoesNotDisableLaterStop verifies
// that a Stop called before Start does not permanently disable
// cancellation. The previous implementation ran stopOnce.Do
// unconditionally, so an early Stop burned the once and every later Stop
// became a no-op on cancellation — the group could then only be stopped
// via the parent context.
func TestSubscriptionGroup_StopBeforeStartDoesNotDisableLaterStop(t *testing.T) {
	cons := &fakeConsumer{}
	sub := messaging.NewSubscription("a", cons, messaging.Binding{},
		func(context.Context, messaging.Delivery) error { return nil },
		messaging.WithSubscriptionLogger(newQuietLogger()),
	)
	g := messaging.NewSubscriptionGroup(newQuietLogger())
	require.NoError(t, g.Add(sub))

	// Stop before Start — must be a harmless no-op, NOT a burn of the
	// group's one-shot cancel.
	preCtx, preCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer preCancel()
	require.NoError(t, g.Stop(preCtx))

	// Start the group under a context we deliberately never cancel, so the
	// only way the group can exit is via a working Stop.
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	startDone := make(chan error, 1)
	go func() { startDone <- g.Start(runCtx) }()

	require.Eventually(t, func() bool {
		return cons.called.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond, "consumer must enter Consume")

	// A later Stop must actually cancel the group. With its own short
	// context: if Stop relies on the parent context (because cancellation
	// was disabled), Start would not return and Stop would block until
	// this ctx times out.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	require.NoError(t, g.Stop(stopCtx))

	select {
	case <-startDone:
		// Group exited because Stop cancelled it. Good.
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop — early Stop disabled cancellation")
	}
}
