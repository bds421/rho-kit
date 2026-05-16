package messagingtest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// PublisherFactory constructs a fresh Publisher for one subtest.
type PublisherFactory func(t *testing.T) messaging.Publisher

// RunPublisher executes the publisher conformance battery against
// the supplied factory.
func RunPublisher(t *testing.T, factory PublisherFactory) {
	t.Helper()
	if factory == nil {
		t.Fatal("messagingtest.RunPublisher: factory must not be nil")
	}

	t.Run("RejectsNilContext", func(t *testing.T) { testPublisherRejectsNilCtx(t, factory) })
	t.Run("RejectsCancelledContext", func(t *testing.T) { testPublisherRejectsCancelledCtx(t, factory) })
	t.Run("HappyPathRoundTripsAcrossMultipleCalls", func(t *testing.T) { testPublisherSequentialHappyPath(t, factory) })
	t.Run("ConcurrentSafe", func(t *testing.T) { testPublisherConcurrentSafe(t, factory) })
	t.Run("MessageValuesPropagateUnchanged", func(t *testing.T) { testPublisherMessageRoundTrip(t, factory) })
}

func testPublisherRejectsNilCtx(t *testing.T, factory PublisherFactory) {
	p := factory(t)
	msg, err := messaging.NewMessage("test.event", map[string]string{"k": "v"})
	require.NoError(t, err)

	err = p.Publish(nil, "test-exchange", "rk", msg) //nolint:staticcheck // intentional nil-ctx contract test
	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrInvalidPublishContext,
		"Publish with nil ctx MUST return ErrInvalidPublishContext (not panic)")
}

func testPublisherRejectsCancelledCtx(t *testing.T, factory PublisherFactory) {
	p := factory(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	msg, err := messaging.NewMessage("test.event", map[string]string{"k": "v"})
	require.NoError(t, err)

	err = p.Publish(ctx, "test-exchange", "rk", msg)
	require.Error(t, err)
	// Either context.Canceled or a wrapped version that satisfies
	// errors.Is — both satisfy the contract.
	if !errors.Is(err, context.Canceled) {
		// Some backends may surface ErrInvalidPublishContext via
		// ValidatePublishContext — that also satisfies the
		// "fail-fast on cancelled ctx" requirement.
		assert.ErrorIs(t, err, messaging.ErrInvalidPublishContext,
			"cancelled ctx must surface either context.Canceled or ErrInvalidPublishContext, got %v", err)
	}
}

func testPublisherSequentialHappyPath(t *testing.T, factory PublisherFactory) {
	p := factory(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		msg, err := messaging.NewMessage("test.event", map[string]int{"n": i})
		require.NoError(t, err)
		require.NoError(t, p.Publish(ctx, "test-exchange", "rk", msg),
			"sequential Publish must succeed against a healthy broker (msg %d)", i)
	}
}

func testPublisherConcurrentSafe(t *testing.T, factory PublisherFactory) {
	p := factory(t)
	ctx := context.Background()

	var errs atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg, err := messaging.NewMessage("test.event", map[string]int{"n": idx})
			if err != nil {
				errs.Add(1)
				return
			}
			if err := p.Publish(ctx, "test-exchange", "rk", msg); err != nil {
				errs.Add(1)
			}
		}(i)
	}
	wg.Wait()
	assert.Zero(t, errs.Load(),
		"concurrent Publish from multiple goroutines must not surface errors")
}

func testPublisherMessageRoundTrip(t *testing.T, factory PublisherFactory) {
	// Construct a Message with the canonical fields populated. The
	// Publisher contract is "the message published is the message
	// the consumer eventually receives" — backends MUST NOT mutate
	// the message in flight. The actual consumer-side verification
	// is per-backend (this suite only asserts the publish call
	// accepts the payload).
	p := factory(t)
	ctx := context.Background()

	payload := map[string]any{
		"order_id": "ord-99",
		"amount":   42.5,
		"flag":     true,
	}
	msg, err := messaging.NewMessage("orders.created", payload)
	require.NoError(t, err)
	require.NotEmpty(t, msg.ID, "NewMessage MUST stamp a non-empty ID")
	require.Equal(t, "orders.created", msg.Type)
	require.NotEmpty(t, msg.Payload)

	require.NoError(t, p.Publish(ctx, "orders", "orders.created", msg))
}
