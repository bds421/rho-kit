package wmconvert_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
	"github.com/bds421/rho-kit/infra/messaging/wmconvert"
)

func TestPublisherAdapter_Publish(t *testing.T) {
	goChan := gochannel.NewGoChannel(gochannel.Config{}, watermill.NopLogger{})
	defer func() { _ = goChan.Close() }()

	adapter := wmconvert.NewPublisherAdapter(goChan, wmconvert.ExchangeTopic)

	// Subscribe to the topic BEFORE publishing.
	msgs, err := goChan.Subscribe(context.Background(), "orders")
	require.NoError(t, err)

	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "order.created",
		Payload: json.RawMessage(`{"id":"abc"}`),
	}

	err = adapter.Publish(context.Background(), "orders", "order.created", msg)
	require.NoError(t, err)

	received := <-msgs
	assert.Equal(t, "msg-1", received.UUID)
	assert.Equal(t, "order.created", received.Metadata.Get(wmconvert.MetaMessageType))
	assert.Equal(t, "orders", received.Metadata.Get(wmconvert.MetaExchange))
	received.Ack()
}

func TestPublisherAdapter_TopicFunctions(t *testing.T) {
	tests := []struct {
		name       string
		fn         wmconvert.TopicFunc
		exchange   string
		routingKey string
		want       string
	}{
		{"exchange", wmconvert.ExchangeTopic, "ex", "rk", "ex"},
		{"routingKey", wmconvert.RoutingKeyTopic, "ex", "rk", "rk"},
		{"combined", wmconvert.CombinedTopic, "ex", "rk", "ex.rk"},
		{"combined_empty_exchange", wmconvert.CombinedTopic, "", "rk", "rk"},
		{"combined_empty_rk", wmconvert.CombinedTopic, "ex", "", "ex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn(tt.exchange, tt.routingKey)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConsumerAdapter_ConsumeOnce(t *testing.T) {
	goChan := gochannel.NewGoChannel(gochannel.Config{
		BlockPublishUntilSubscriberAck: true,
	}, watermill.NopLogger{})
	defer func() { _ = goChan.Close() }()

	adapter := wmconvert.NewConsumerAdapter(goChan, nil)

	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			Exchange:     "orders",
			ExchangeType: "direct",
			Queue:        "orders-queue",
			RoutingKey:   "order.created",
		},
	}

	var received messaging.Delivery
	var wg sync.WaitGroup
	wg.Add(1)

	ctx, cancel := context.WithCancel(context.Background())

	// Start consumer first, then publish.
	consumerReady := make(chan struct{})
	go func() {
		close(consumerReady)
		_ = adapter.ConsumeOnce(ctx, binding, func(_ context.Context, d messaging.Delivery) error {
			received = d
			wg.Done()
			cancel() // stop after first message
			return nil
		})
	}()
	<-consumerReady

	// Publish a message via GoChannel using the queue name as topic.
	wmMsg := wmconvert.ToWatermill(messaging.Message{
		ID:      "msg-1",
		Type:    "order.created",
		Payload: json.RawMessage(`{"id":"abc"}`),
	}, "orders", "order.created")

	err := goChan.Publish("orders-queue", wmMsg)
	require.NoError(t, err)

	wg.Wait()
	assert.Equal(t, "msg-1", received.Message.ID)
	assert.Equal(t, "order.created", received.Message.Type)
	assert.Equal(t, "orders", received.Exchange)
}

func TestConsumerAdapter_ContextCancellation(t *testing.T) {
	goChan := gochannel.NewGoChannel(gochannel.Config{}, watermill.NopLogger{})
	defer func() { _ = goChan.Close() }()

	adapter := wmconvert.NewConsumerAdapter(goChan, nil)

	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			Exchange:     "test",
			ExchangeType: "direct",
			Queue:        "test-queue",
			RoutingKey:   "test.event",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		_ = adapter.ConsumeOnce(ctx, binding, func(_ context.Context, _ messaging.Delivery) error {
			return nil
		})
		close(done)
	}()

	// Cancel context — ConsumeOnce should return.
	cancel()
	<-done
}

func TestConnectorAdapter(t *testing.T) {
	healthy := true
	connector := wmconvert.NewConnectorAdapter(
		func() bool { return healthy },
		func() error { return nil },
	)

	assert.True(t, connector.Healthy())

	healthy = false
	assert.False(t, connector.Healthy())

	assert.NoError(t, connector.Close())
}

func TestPublisherAdapter_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		wmconvert.NewPublisherAdapter(nil, nil)
	})
}

func TestConsumerAdapter_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		wmconvert.NewConsumerAdapter(nil, nil)
	})
}
