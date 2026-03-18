package redisstream

import (
	"context"
	"log/slog"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Contains(t, err.Error(), "exceeds max")
}

func TestProducer_PublishBatch_Empty(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	producer := NewProducer(client)
	ids, err := producer.PublishBatch(context.Background(), "test:stream", nil)
	require.NoError(t, err)
	assert.Nil(t, ids)
}

func TestWithMaxStreamLen_IgnoresNegative(t *testing.T) {
	p := &Producer{}
	WithMaxStreamLen(-1)(p)
	assert.Equal(t, int64(0), p.maxLen)
}

func TestWithProducerMaxPayloadSize_IgnoresNegative(t *testing.T) {
	p := &Producer{maxPayloadSize: defaultStreamMaxPayloadSize}
	WithProducerMaxPayloadSize(-1)(p)
	assert.Equal(t, defaultStreamMaxPayloadSize, p.maxPayloadSize)
}
