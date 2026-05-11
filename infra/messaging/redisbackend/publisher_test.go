package redisbackend

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	stream "github.com/bds421/rho-kit/data/stream/redisstream/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestNewPublisher_PanicsOnNilProducer(t *testing.T) {
	assert.Panics(t, func() {
		NewPublisher(nil)
	})
}

func TestNewPublisher_PanicsOnNilOption(t *testing.T) {
	producer, closeFn := testProducer(t)
	defer closeFn()

	assert.Panics(t, func() {
		NewPublisher(producer, nil)
	})
}

func TestPublisher_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		p    *Publisher
	}{
		{"nil", nil},
		{"zero", &Publisher{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.Publish(ctx, "stream", "rk", messaging.Message{})
			assert.ErrorIs(t, err, messaging.ErrInvalidPublisher)
		})
	}
}

func TestPublisher_ContextAndRouteRejectedBeforePublish(t *testing.T) {
	producer, closeFn := testProducer(t)
	defer closeFn()
	pub := NewPublisher(producer)
	msg := messaging.Message{ID: "msg-1", Type: "test.event", Payload: json.RawMessage(`{}`)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := pub.Publish(ctx, "stream", "test.event", msg)
	assert.ErrorIs(t, err, context.Canceled)

	err = pub.Publish(context.Background(), "stream\nprod", "test.event", msg)
	assert.ErrorIs(t, err, messaging.ErrInvalidRoute)
}

func TestPublisher_MaxMessageBytesRejects(t *testing.T) {
	producer, closeFn := testProducer(t)
	defer closeFn()
	pub := NewPublisher(producer, WithMaxMessageBytes(32))
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "large.event",
		Payload: json.RawMessage(`"this payload is intentionally too large"`),
	}

	err := pub.Publish(context.Background(), "stream", "large.event", msg)

	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrMessageTooLarge)
}

func TestPublisher_RouteMaxMessageBytesOverridesDefault(t *testing.T) {
	producer, closeFn := testProducer(t)
	defer closeFn()
	pub := NewPublisher(producer,
		WithMaxMessageBytes(32),
		WithRouteMaxMessageBytes("stream", "large.event", 256),
	)
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "large.event",
		Payload: json.RawMessage(`"this payload passes the route override"`),
	}

	err := pub.Publish(context.Background(), "stream", "large.event", msg)

	require.NoError(t, err)
}

func TestPublisher_InvalidHeadersRejectedBeforePublish(t *testing.T) {
	producer, closeFn := testProducer(t)
	defer closeFn()
	pub := NewPublisher(producer)
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: json.RawMessage(`{}`),
		Headers: map[string]string{"X-Trace": "bad\r\nvalue"},
	}

	err := pub.Publish(context.Background(), "stream", "test.event", msg)

	require.Error(t, err)
	assert.True(t, errors.Is(err, messaging.ErrInvalidMessageHeader))
}

func TestPublisher_InvalidMessageRejectedBeforePublish(t *testing.T) {
	producer, closeFn := testProducer(t)
	defer closeFn()
	pub := NewPublisher(producer)
	msg := messaging.Message{
		ID:      "",
		Type:    "test.event",
		Payload: json.RawMessage(`{}`),
	}

	err := pub.Publish(context.Background(), "stream", "test.event", msg)

	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrInvalidMessage)
}

func testProducer(t *testing.T) (*stream.Producer, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	return stream.NewProducer(client), func() { _ = client.Close() }
}
