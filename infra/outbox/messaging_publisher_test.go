package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/infra/v2/outbox"
)

type fakeMessagingPublisher struct {
	gotExchange   string
	gotRoutingKey string
	gotMessage    messaging.Message
	err           error
}

func (f *fakeMessagingPublisher) Publish(_ context.Context, exchange, routingKey string, msg messaging.Message) error {
	f.gotExchange = exchange
	f.gotRoutingKey = routingKey
	f.gotMessage = msg
	return f.err
}

func TestNewMessagingPublisher_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { outbox.NewMessagingPublisher(nil) })
}

func TestMessagingPublisher_ConvertsEntryFields(t *testing.T) {
	inner := &fakeMessagingPublisher{}
	pub := outbox.NewMessagingPublisher(inner)

	headers := []byte(`{"x-correlation-id":"abc"}`)
	entry := outbox.Entry{
		ID:          uuid.New(),
		Topic:       "orders",
		RoutingKey:  "order.created",
		MessageID:   "msg-1",
		MessageType: "OrderCreated",
		Payload:     json.RawMessage(`{"order_id":42}`),
		Headers:     headers,
	}

	require.NoError(t, pub.Publish(context.Background(), entry))
	assert.Equal(t, "orders", inner.gotExchange)
	assert.Equal(t, "order.created", inner.gotRoutingKey)
	assert.Equal(t, "msg-1", inner.gotMessage.ID)
	assert.Equal(t, "OrderCreated", inner.gotMessage.Type)
	assert.JSONEq(t, `{"order_id":42}`, string(inner.gotMessage.Payload))
	assert.Equal(t, "abc", inner.gotMessage.Headers["x-correlation-id"])
}

func TestMessagingPublisher_NilHeadersOK(t *testing.T) {
	inner := &fakeMessagingPublisher{}
	pub := outbox.NewMessagingPublisher(inner)

	entry := outbox.Entry{Topic: "x", RoutingKey: "y", Payload: json.RawMessage(`{}`)}
	require.NoError(t, pub.Publish(context.Background(), entry))
	assert.Nil(t, inner.gotMessage.Headers)
}

func TestMessagingPublisher_PropagatesBackendError(t *testing.T) {
	boom := errors.New("kafka unreachable")
	inner := &fakeMessagingPublisher{err: boom}
	pub := outbox.NewMessagingPublisher(inner)

	err := pub.Publish(context.Background(), outbox.Entry{Topic: "t", Payload: json.RawMessage(`{}`)})
	require.Error(t, err)
	assert.ErrorIs(t, err, boom, "backend error must remain matchable via errors.Is")
	assert.Contains(t, err.Error(), "outbox: messaging publisher delegate",
		"kit-controlled prefix must be visible for log triage")
}

func TestMessagingPublisher_InvalidHeadersBubble(t *testing.T) {
	inner := &fakeMessagingPublisher{}
	pub := outbox.NewMessagingPublisher(inner)

	entry := outbox.Entry{Topic: "t", Headers: []byte(`not-json`)}
	err := pub.Publish(context.Background(), entry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode headers")
}
