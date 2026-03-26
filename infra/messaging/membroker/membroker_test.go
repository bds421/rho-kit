package membroker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
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

func TestBroker_ImplementsMessagePublisher(t *testing.T) {
	var _ messaging.MessagePublisher = (*Broker)(nil)
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

	assert.Equal(t, 3, received.SchemaVersion)
	assert.Equal(t, 3, received.Message.SchemaVersion)
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

	assert.Equal(t, 0, received.SchemaVersion)
	assert.Equal(t, 0, received.Message.SchemaVersion)
}
