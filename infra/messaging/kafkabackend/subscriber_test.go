package kafkabackend

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestNewSubscriber_RejectsEmptyGroup(t *testing.T) {
	_, err := NewSubscriber([]string{"localhost:9092"}, "", []string{"events"})
	require.Error(t, err)
}

func TestNewSubscriber_RejectsEmptyTopics(t *testing.T) {
	_, err := NewSubscriberWithConfig(Config{
		Brokers:       []string{"localhost:9092"},
		AllowInsecure: true,
	}, "g", nil)
	require.Error(t, err)
}

func TestNewSubscriber_RejectsBlankTopic(t *testing.T) {
	_, err := NewSubscriberWithConfig(Config{
		Brokers:       []string{"localhost:9092"},
		AllowInsecure: true,
	}, "g", []string{"", "events"})
	require.Error(t, err)
}

func TestNewSubscriber_PreservesGroupAndTopics(t *testing.T) {
	sub, err := NewSubscriberWithConfig(Config{
		Brokers:       []string{"localhost:9092"},
		AllowInsecure: true,
	}, "orders", []string{"events", "audit"})
	require.NoError(t, err)
	assert.Equal(t, "orders", sub.Group())
	assert.Equal(t, []string{"events", "audit"}, sub.Topics())
}

func TestSubscriber_Consume_ValidatesBinding(t *testing.T) {
	sub := mustSubscriber(t)
	err := sub.Consume(context.Background(), messaging.Binding{
		BindingSpec: messaging.BindingSpec{Exchange: ""},
	}, func(context.Context, messaging.Delivery) error { return nil })
	require.ErrorIs(t, err, messaging.ErrInvalidRoute)
}

func TestSubscriber_Consume_RejectsForeignTopic(t *testing.T) {
	sub := mustSubscriber(t)
	err := sub.Consume(context.Background(), messaging.Binding{
		BindingSpec: messaging.BindingSpec{Exchange: "other"},
	}, func(context.Context, messaging.Delivery) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in the subscriber topic set")
}

func TestSubscriber_Consume_RejectsMismatchedQueue(t *testing.T) {
	sub := mustSubscriber(t)
	err := sub.Consume(context.Background(), messaging.Binding{
		BindingSpec: messaging.BindingSpec{Exchange: "events", Queue: "other-group"},
	}, func(context.Context, messaging.Delivery) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match subscriber group")
}

func TestSubscriber_Consume_NilHandlerErrors(t *testing.T) {
	sub := mustSubscriber(t)
	err := sub.Consume(context.Background(), messaging.Binding{
		BindingSpec: messaging.BindingSpec{Exchange: "events"},
	}, nil)
	assert.ErrorIs(t, err, messaging.ErrInvalidConsumer)
}

func mustSubscriber(t *testing.T) *Subscriber {
	t.Helper()
	sub, err := NewSubscriberWithConfig(Config{
		Brokers:       []string{"localhost:9092"},
		AllowInsecure: true,
	}, "orders", []string{"events"})
	require.NoError(t, err)
	return sub
}
