package redisstream

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kitstream "github.com/bds421/rho-kit/data/v2/stream"
)

func newTestClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	mr := miniredis.RunT(t)
	return goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
}

func TestProducer_Publish(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	producer := NewProducer(client, WithProducerLogger(slog.Default()))
	ctx := context.Background()

	msg, err := NewMessage("test.created", map[string]string{"key": "value"})
	require.NoError(t, err)

	redisID, err := producer.Publish(ctx, "test:stream", msg)
	require.NoError(t, err)
	assert.NotEmpty(t, redisID)
}

func TestProducer_PublishRedisErrorDoesNotReflectStreamName(t *testing.T) {
	client := newTestClient(t)
	producer := NewProducer(client, WithProducerLogger(slog.Default()))
	msg, err := NewMessage("test.created", map[string]string{"key": "value"})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, err = producer.Publish(context.Background(), "test:stream:secret-token", msg)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestStreamMetricLabelDoesNotExposeStreamName(t *testing.T) {
	label := streamMetricLabel("tenant-secret:events.high")
	assert.Regexp(t, `^stream-[0-9a-f]{12}$`, label)
	assert.NotContains(t, label, "tenant")
	assert.NotContains(t, label, "secret")
	assert.NotContains(t, label, "events")
	assert.NotContains(t, label, "high")
	assert.Equal(t, label, streamMetricLabel("tenant-secret:events.high"))
}

func TestNewProducer_PanicsOnNilOption(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	assert.Panics(t, func() {
		NewProducer(client, nil)
	})
}

func TestProducer_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	msg := Message{ID: "msg-1", Type: "test", Payload: []byte(`{}`), Timestamp: time.Now()}
	cases := []struct {
		name     string
		producer *Producer
	}{
		{"nil", nil},
		{"zero", &Producer{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.producer.Publish(ctx, "test:stream", msg)
			assert.ErrorIs(t, err, kitstream.ErrInvalidStream)

			_, err = tc.producer.PublishBatch(ctx, "test:stream", []Message{msg})
			assert.ErrorIs(t, err, kitstream.ErrInvalidStream)
		})
	}
}

func TestProducer_Publish_EmptyStream(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	producer := NewProducer(client)
	msg, _ := NewMessage("test", "data")

	_, err := producer.Publish(context.Background(), "", msg)
	assert.Error(t, err)
}

func TestProducer_Publish_PayloadTooLarge(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	producer := NewProducer(client, WithProducerMaxPayloadSize(10))

	msg := Message{
		Type:    "test",
		Payload: make([]byte, 20),
	}

	_, err := producer.Publish(context.Background(), "test:stream", msg)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrMessageTooLarge)
}

func TestProducer_Publish_RejectsInvalidMessage(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	producer := NewProducer(client)
	msg := Message{
		ID:      "bad id",
		Type:    "test",
		Payload: []byte(`{}`),
	}

	_, err := producer.Publish(context.Background(), "test:stream", msg)
	assert.ErrorIs(t, err, ErrInvalidMessage)
}

func TestProducer_Publish_InvalidHeaders(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	producer := NewProducer(client)
	msg := Message{
		Type:    "test",
		Payload: []byte(`{}`),
		Headers: map[string]string{"Bad Header": "value"},
	}

	_, err := producer.Publish(context.Background(), "test:stream", msg)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidHeader))
}

func TestProducer_PublishBatch_Empty(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	producer := NewProducer(client)
	ids, err := producer.PublishBatch(context.Background(), "test:stream", nil)
	require.NoError(t, err)
	assert.Nil(t, ids)
}

func TestProducer_PublishBatch_RejectsTooManyMessages(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	producer := NewProducer(client)
	msgs := make([]Message, MaxBatchMessages+1)
	for i := range msgs {
		msg, err := NewMessage("test.created", map[string]int{"i": i})
		require.NoError(t, err)
		msgs[i] = msg
	}

	ids, err := producer.PublishBatch(context.Background(), "test:stream", msgs)
	assert.ErrorIs(t, err, ErrBatchTooLarge)
	assert.Nil(t, ids)

	n, err := client.XLen(context.Background(), "test:stream").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestProducerOptions_PanicOnInvalid(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithMaxStreamLen negative": func() { WithMaxStreamLen(-1) },
		"WithProducerMaxPayloadSize negative": func() {
			WithProducerMaxPayloadSize(-1)
		},
		"WithRetention zero":     func() { WithRetention(0) },
		"WithRetention negative": func() { WithRetention(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, fn)
		})
	}
}
