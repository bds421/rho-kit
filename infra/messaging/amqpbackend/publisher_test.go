package amqpbackend

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestNewPublisher_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		NewPublisher(noopConnector{}, discardLogger(), nil)
	})
}

func TestPublisher_InvalidReceiverReturnsError(t *testing.T) {
	msg := messaging.Message{ID: "msg-1", Type: "test.event", Payload: json.RawMessage(`{}`)}

	var nilPublisher *Publisher
	assert.ErrorIs(t, nilPublisher.Publish(context.Background(), "events", "test.event", msg), messaging.ErrInvalidPublisher)
	assert.ErrorIs(t, (&Publisher{}).Publish(context.Background(), "events", "test.event", msg), messaging.ErrInvalidPublisher)
	assert.ErrorIs(t, (&Publisher{}).PublishRaw(context.Background(), "events", "test.event", []byte(`{}`), "msg-1"), messaging.ErrInvalidPublisher)
}

func TestPublisher_ContextAndRouteRejectedBeforeChannelOpen(t *testing.T) {
	pub := NewPublisher(noopConnector{}, discardLogger())
	msg := messaging.Message{ID: "msg-1", Type: "test.event", Payload: json.RawMessage(`{}`)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := pub.Publish(ctx, "events", "test.event", msg)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "open channel")

	err = pub.Publish(context.Background(), "events\nprod", "test.event", msg)
	assert.ErrorIs(t, err, messaging.ErrInvalidRoute)
	assert.NotContains(t, err.Error(), "open channel")
}

func TestPublisher_MaxMessageBytesRejectsBeforeChannelOpen(t *testing.T) {
	pub := NewPublisher(noopConnector{}, discardLogger(), WithMaxMessageBytes(32))
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "large.event",
		Payload: json.RawMessage(`"this payload is intentionally too large"`),
	}

	err := pub.Publish(context.Background(), "events", "large.event", msg)

	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrMessageTooLarge)
}

func TestPublisher_RouteMaxMessageBytesOverridesDefault(t *testing.T) {
	pub := NewPublisher(noopConnector{}, discardLogger(),
		WithMaxMessageBytes(32),
		WithRouteMaxMessageBytes("events", "large.event", 256),
	)
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "large.event",
		Payload: json.RawMessage(`"this payload passes the route override"`),
	}

	err := pub.Publish(context.Background(), "events", "large.event", msg)

	require.Error(t, err)
	assert.NotErrorIs(t, err, messaging.ErrMessageTooLarge)
	assert.Contains(t, err.Error(), "open channel")
}

func TestPublisher_InvalidHeadersRejectedBeforeChannelOpen(t *testing.T) {
	pub := NewPublisher(noopConnector{}, discardLogger())
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: json.RawMessage(`{}`),
		Headers: map[string]string{"Bad Header": "value"},
	}

	err := pub.Publish(context.Background(), "events", "test.event", msg)

	require.Error(t, err)
	assert.True(t, errors.Is(err, messaging.ErrInvalidMessageHeader))
	assert.NotContains(t, err.Error(), "open channel")
}

func TestPublisher_InvalidMessageRejectedBeforeChannelOpen(t *testing.T) {
	pub := NewPublisher(noopConnector{}, discardLogger())
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "bad\nevent",
		Payload: json.RawMessage(`{}`),
	}

	err := pub.Publish(context.Background(), "events", "test.event", msg)

	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrInvalidMessage)
	assert.NotContains(t, err.Error(), "open channel")
}

func TestPublisher_PublishRawAppliesSizeLimit(t *testing.T) {
	pub := NewPublisher(noopConnector{}, discardLogger(), WithMaxMessageBytes(4))

	err := pub.PublishRaw(context.Background(), "events", "large.event", []byte("too-large"), "msg-1")

	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrMessageTooLarge)
}
