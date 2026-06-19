package membroker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestBroker_PublishAndDrain(t *testing.T) {
	b := New()

	var received []messaging.Delivery
	b.Subscribe("events", "user.created", func(_ context.Context, d messaging.Delivery) error {
		received = append(received, d)
		return nil
	})

	msg, err := messaging.NewMessage("user.created", map[string]string{"id": "123"})
	require.NoError(t, err)

	err = b.Publish(context.Background(), "events", "user.created", msg)
	require.NoError(t, err)

	err = b.Drain(context.Background())
	require.NoError(t, err)

	assert.Len(t, received, 1)
	assert.Equal(t, "user.created", received[0].Message.Type)
	assert.Equal(t, "events", received[0].Exchange)
}

func TestBroker_ConcurrentPublishAndDrainDeliversEachMessageOnce(t *testing.T) {
	b := New()

	const messageCount = 200

	var mu sync.Mutex
	deliveries := make(map[string]int, messageCount)
	b.Subscribe("events", "user.created", func(_ context.Context, d messaging.Delivery) error {
		mu.Lock()
		deliveries[d.Message.ID]++
		mu.Unlock()
		return nil
	})

	var wg sync.WaitGroup
	wg.Add(messageCount)
	for i := 0; i < messageCount; i++ {
		go func(i int) {
			defer wg.Done()
			msg, err := messaging.NewMessage("user.created", map[string]int{"i": i})
			require.NoError(t, err)
			require.NoError(t, b.PublishAndDrain(context.Background(), "events", "user.created", msg))
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, messageCount, "every published message must be delivered exactly once")
	for id, count := range deliveries {
		assert.Equalf(t, 1, count, "message %s delivered %d times, want exactly once", id, count)
	}
}

func TestBroker_MaxMessageBytesRejects(t *testing.T) {
	b := New(WithMaxMessageBytes(32))
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "large.event",
		Payload: json.RawMessage(`"this payload is intentionally too large"`),
	}

	err := b.Publish(context.Background(), "events", "large.event", msg)

	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrMessageTooLarge)
}

func TestBroker_RouteMaxMessageBytesOverridesDefault(t *testing.T) {
	b := New(WithMaxMessageBytes(32), WithRouteMaxMessageBytes("events", "large.event", 256))
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "large.event",
		Payload: json.RawMessage(`"this payload passes the route override"`),
	}

	err := b.Publish(context.Background(), "events", "large.event", msg)

	require.NoError(t, err)
	assert.Len(t, b.Published(), 1)
}

func TestBroker_InvalidHeadersRejected(t *testing.T) {
	b := New()
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: json.RawMessage(`{}`),
		Headers: map[string]string{"Bad Header": "value"},
	}

	err := b.Publish(context.Background(), "events", "test.event", msg)

	require.Error(t, err)
	assert.True(t, errors.Is(err, messaging.ErrInvalidMessageHeader))
	assert.Empty(t, b.Published())
}

func TestBroker_InvalidMessageRejected(t *testing.T) {
	b := New()
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "bad event",
		Payload: json.RawMessage(`{}`),
	}

	err := b.Publish(context.Background(), "events", "test.event", msg)

	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrInvalidMessage)
	assert.Empty(t, b.Published())
}

func TestBroker_PublishRejectsCanceledContext(t *testing.T) {
	b := New()
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = b.Publish(ctx, "events", "test.event", msg)

	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, b.Published())
}

func TestBroker_PublishRejectsInvalidRoute(t *testing.T) {
	b := New()
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	err = b.Publish(context.Background(), "events\nprod", "test.event", msg)

	assert.ErrorIs(t, err, messaging.ErrInvalidRoute)
	assert.Empty(t, b.Published())
}

func TestBroker_SubscribeRejectsInvalidRoute(t *testing.T) {
	b := New()
	handler := func(context.Context, messaging.Delivery) error { return nil }

	assert.Panics(t, func() { b.Subscribe("events prod", "#", handler) })
	assert.Panics(t, func() { b.Subscribe("events", "bad key", handler) })
}

func TestBroker_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		New(nil)
	})
}

func TestBroker_WildcardSubscribe(t *testing.T) {
	b := New()

	var received []messaging.Delivery
	b.Subscribe("*", "*", func(_ context.Context, d messaging.Delivery) error {
		received = append(received, d)
		return nil
	})

	msg, _ := messaging.NewMessage("test", nil)
	_ = b.Publish(context.Background(), "ex1", "key1", msg)
	_ = b.Publish(context.Background(), "ex2", "key2", msg)

	err := b.Drain(context.Background())
	require.NoError(t, err)

	assert.Len(t, received, 2)
}

func TestBroker_NoMatchingSubscriber(t *testing.T) {
	b := New()

	b.Subscribe("events", "user.created", func(_ context.Context, _ messaging.Delivery) error {
		t.Fatal("should not be called")
		return nil
	})

	msg, _ := messaging.NewMessage("order.placed", nil)
	_ = b.Publish(context.Background(), "events", "order.placed", msg)

	err := b.Drain(context.Background())
	require.NoError(t, err)
}

func TestBroker_HandlerError(t *testing.T) {
	b := New()

	expectedErr := errors.New("handler failed")
	b.Subscribe("*", "*", func(_ context.Context, _ messaging.Delivery) error {
		return expectedErr
	})

	msg, _ := messaging.NewMessage("test", nil)
	_ = b.Publish(context.Background(), "ex", "key", msg)

	err := b.Drain(context.Background())
	assert.ErrorIs(t, err, expectedErr)
}

func TestBroker_DrainErrorPreservesPendingMessages(t *testing.T) {
	b := New()

	fail := true
	var received []string
	b.Subscribe("*", "#", func(_ context.Context, d messaging.Delivery) error {
		received = append(received, d.Message.Type)
		if fail {
			return errors.New("temporary failure")
		}
		return nil
	})

	msg1, err := messaging.NewMessage("type1", nil)
	require.NoError(t, err)
	msg2, err := messaging.NewMessage("type2", nil)
	require.NoError(t, err)
	require.NoError(t, b.Publish(context.Background(), "ex", "type1", msg1))
	require.NoError(t, b.Publish(context.Background(), "ex", "type2", msg2))

	err = b.Drain(context.Background())
	require.Error(t, err)
	assert.Len(t, b.Published(), 2, "failed drain must leave failed and unprocessed messages pending")

	fail = false
	require.NoError(t, b.Drain(context.Background()))
	assert.Empty(t, b.Published())
	assert.Equal(t, []string{"type1", "type1", "type2"}, received,
		"retry should re-dispatch the failed message before continuing")
}

func TestBroker_DrainRejectsCanceledContextBeforeDispatch(t *testing.T) {
	b := New()
	b.Subscribe("*", "#", func(_ context.Context, _ messaging.Delivery) error {
		t.Fatal("handler should not run after context cancellation")
		return nil
	})
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)
	require.NoError(t, b.Publish(context.Background(), "ex", "test.event", msg))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = b.Drain(ctx)

	assert.ErrorIs(t, err, context.Canceled)
	assert.Len(t, b.Published(), 1)
}

func TestBroker_Published(t *testing.T) {
	b := New()

	msg1, _ := messaging.NewMessage("type1", nil)
	msg2, _ := messaging.NewMessage("type2", nil)

	_ = b.Publish(context.Background(), "ex", "k1", msg1)
	_ = b.Publish(context.Background(), "ex", "k2", msg2)

	published := b.Published()
	assert.Len(t, published, 2)
	assert.Equal(t, "type1", published[0].Type)
	assert.Equal(t, "type2", published[1].Type)
}

func TestBroker_PublishedReturnsMessageCopies(t *testing.T) {
	b := New()
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: []byte(`{"key":"value"}`),
		Headers: map[string]string{"X-Trace-Id": "trace-1"},
	}
	require.NoError(t, b.Publish(context.Background(), "ex", "key", msg))

	msg.Payload[8] = 'X'
	msg.Headers["X-Trace-Id"] = "mutated"

	published := b.Published()
	require.Len(t, published, 1)
	assert.JSONEq(t, `{"key":"value"}`, string(published[0].Payload))
	assert.Equal(t, "trace-1", published[0].Headers["X-Trace-Id"])

	published[0].Payload[8] = 'Y'
	published[0].Headers["X-Trace-Id"] = "published-mutated"
	published = b.Published()
	require.Len(t, published, 1)
	assert.JSONEq(t, `{"key":"value"}`, string(published[0].Payload))
	assert.Equal(t, "trace-1", published[0].Headers["X-Trace-Id"])
}

func TestBroker_DrainPassesMessageCopiesToHandlers(t *testing.T) {
	b := New()
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: []byte(`{"key":"value"}`),
		Headers: map[string]string{"X-Trace-Id": "trace-1"},
	}
	b.Subscribe("*", "*", func(_ context.Context, d messaging.Delivery) error {
		d.Message.Payload[8] = 'X'
		d.Message.Headers["X-Trace-Id"] = "mutated"
		return errors.New("keep pending")
	})
	require.NoError(t, b.Publish(context.Background(), "ex", "key", msg))

	err := b.Drain(context.Background())
	require.Error(t, err)
	published := b.Published()
	require.Len(t, published, 1)
	assert.JSONEq(t, `{"key":"value"}`, string(published[0].Payload))
	assert.Equal(t, "trace-1", published[0].Headers["X-Trace-Id"])
}

func TestBroker_Reset(t *testing.T) {
	b := New()

	b.Subscribe("*", "*", func(_ context.Context, _ messaging.Delivery) error { return nil })
	msg, _ := messaging.NewMessage("test", nil)
	_ = b.Publish(context.Background(), "ex", "key", msg)

	b.Reset()

	assert.Empty(t, b.Published())

	// After reset, drain should be a no-op
	err := b.Drain(context.Background())
	require.NoError(t, err)
}

func TestBroker_ImplementsPublisher(t *testing.T) {
	var _ messaging.Publisher = (*Broker)(nil)
}

func TestBroker_InvalidReceiverAndHandlerValidation(t *testing.T) {
	var b *Broker
	msg, err := messaging.NewMessage("test", nil)
	require.NoError(t, err)

	assert.ErrorIs(t, b.Publish(context.Background(), "ex", "key", msg), messaging.ErrInvalidPublisher)
	assert.ErrorIs(t, b.Drain(context.Background()), messaging.ErrInvalidPublisher)
	assert.Nil(t, b.Published())
	assert.NotPanics(t, func() { b.Unsubscribe(1) })
	assert.NotPanics(t, b.Reset)
	assert.Panics(t, func() {
		b.Subscribe("*", "*", func(context.Context, messaging.Delivery) error { return nil })
	})

	b = New()
	assert.Panics(t, func() { b.Subscribe("*", "*", nil) })
}

func TestBroker_Drain_PropagatesSchemaVersion(t *testing.T) {
	b := New()

	var received messaging.Delivery
	b.Subscribe("*", "#", func(_ context.Context, d messaging.Delivery) error {
		received = d
		return nil
	})

	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)
	msg = msg.WithSchemaVersion(3)

	err = b.PublishAndDrain(context.Background(), "ex", "test.event", msg)
	require.NoError(t, err)

	assert.Equal(t, uint(3), received.SchemaVersion)
	assert.Equal(t, uint(3), received.Message.SchemaVersion)
}

func TestBroker_Drain_UnversionedMessage(t *testing.T) {
	b := New()

	var received messaging.Delivery
	b.Subscribe("*", "#", func(_ context.Context, d messaging.Delivery) error {
		received = d
		return nil
	})

	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	err = b.PublishAndDrain(context.Background(), "ex", "test.event", msg)
	require.NoError(t, err)

	assert.Equal(t, uint(0), received.SchemaVersion)
	assert.Equal(t, uint(0), received.Message.SchemaVersion)
}
