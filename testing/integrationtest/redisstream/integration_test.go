//go:build integration

package redisstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/data/stream/redisstream/v2"
	"github.com/bds421/rho-kit/infra/redis/redistest/v2"
	"github.com/bds421/rho-kit/infra/redis/v2"
)

func redisClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	url := redistest.Start(t)
	opts, err := goredis.ParseURL(url)
	require.NoError(t, err)
	conn, err := redis.Connect(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn.Client()
}

// --- Stream Producer Tests ---

func TestProducer_LiveRedis_Publish(t *testing.T) {
	client := redisClient(t)

	producer := redisstream.NewProducer(client, redisstream.WithProducerLogger(slog.Default()))
	ctx := context.Background()
	stream := fmt.Sprintf("test:stream:%d", time.Now().UnixNano())

	msg, err := redisstream.NewMessage("test.created", map[string]string{"key": "value"})
	require.NoError(t, err)

	redisID, err := producer.Publish(ctx, stream, msg)
	require.NoError(t, err)
	assert.NotEmpty(t, redisID)

	// Verify the message is in the stream.
	msgs, err := client.XRange(ctx, stream, "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, msg.ID, msgs[0].Values["id"])
	assert.Equal(t, "test.created", msgs[0].Values["type"])
}

func TestProducer_LiveRedis_PublishBatch(t *testing.T) {
	client := redisClient(t)

	producer := redisstream.NewProducer(client)
	ctx := context.Background()
	stream := fmt.Sprintf("test:stream:batch:%d", time.Now().UnixNano())

	var msgs []redisstream.Message
	for i := range 5 {
		m, err := redisstream.NewMessage("batch.item", map[string]int{"index": i})
		require.NoError(t, err)
		msgs = append(msgs, m)
	}

	ids, err := producer.PublishBatch(ctx, stream, msgs)
	require.NoError(t, err)
	assert.Len(t, ids, 5)

	// Verify all messages are in the stream.
	entries, err := client.XRange(ctx, stream, "-", "+").Result()
	require.NoError(t, err)
	assert.Len(t, entries, 5)
}

func TestProducer_LiveRedis_MaxLen(t *testing.T) {
	client := redisClient(t)

	producer := redisstream.NewProducer(client, redisstream.WithMaxStreamLen(50))
	ctx := context.Background()
	stream := fmt.Sprintf("test:stream:maxlen:%d", time.Now().UnixNano())

	for range 200 {
		msg, err := redisstream.NewMessage("trim.test", "data")
		require.NoError(t, err)
		_, err = producer.Publish(ctx, stream, msg)
		require.NoError(t, err)
	}

	// With approximate trimming (~), Redis trims in chunks of radix tree nodes.
	// The actual count will be roughly around maxLen but not exact.
	length, err := client.XLen(ctx, stream).Result()
	require.NoError(t, err)
	assert.Less(t, length, int64(200)) // should be significantly less than 200
}

// --- Stream Consumer Tests ---

func TestConsumer_LiveRedis_ConsumeAndAck(t *testing.T) {
	client := redisClient(t)

	producer := redisstream.NewProducer(client)
	ctx := context.Background()
	stream := fmt.Sprintf("test:consume:%d", time.Now().UnixNano())
	group := "test-group"

	// Publish a message.
	msg, err := redisstream.NewMessage("consume.test", map[string]string{"data": "hello"})
	require.NoError(t, err)
	_, err = producer.Publish(ctx, stream, msg)
	require.NoError(t, err)

	// Consume it.
	var received redisstream.Message
	var wg sync.WaitGroup
	wg.Add(1)
	var once sync.Once

	consumer, err := redisstream.NewConsumer(client, group,
		redisstream.WithConsumerLogger(slog.Default()),
		redisstream.WithBlockDuration(time.Second),
	)
	require.NoError(t, err)

	consumeCtx, cancel := context.WithCancel(ctx)

	go consumer.Consume(consumeCtx, stream, func(_ context.Context, m redisstream.Message) error {
		received = m
		// Guard against redelivery double-Done panicking the WaitGroup.
		once.Do(wg.Done)
		cancel()
		return nil
	})

	wg.Wait()

	assert.Equal(t, msg.ID, received.ID)
	assert.Equal(t, "consume.test", received.Type)

	var payload map[string]string
	require.NoError(t, json.Unmarshal(received.Payload, &payload))
	assert.Equal(t, "hello", payload["data"])
}

func TestConsumer_LiveRedis_DeadLetter_PermanentError(t *testing.T) {
	client := redisClient(t)

	producer := redisstream.NewProducer(client)
	ctx := context.Background()
	stream := fmt.Sprintf("test:deadletter:perm:%d", time.Now().UnixNano())
	group := "test-group-dl"
	dlStream := stream + ".dead"

	msg, err := redisstream.NewMessage("dl.test", "payload")
	require.NoError(t, err)
	_, err = producer.Publish(ctx, stream, msg)
	require.NoError(t, err)

	consumer, err := redisstream.NewConsumer(client, group,
		redisstream.WithConsumerLogger(slog.Default()),
		redisstream.WithBlockDuration(time.Second),
	)
	require.NoError(t, err)

	consumeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	go consumer.Consume(consumeCtx, stream, func(_ context.Context, _ redisstream.Message) error {
		return apperror.NewPermanentWithCause("permanent failure", errors.New("test"))
	})

	// Poll until the message appears in the dead-letter stream.
	require.Eventually(t, func() bool {
		dlMsgs, err := client.XRange(ctx, dlStream, "-", "+").Result()
		return err == nil && len(dlMsgs) > 0
	}, 10*time.Second, 100*time.Millisecond)

	cancel()

	// Verify message is in dead-letter stream with correct reason.
	dlMsgs, err := client.XRange(ctx, dlStream, "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, dlMsgs, 1)
	assert.Equal(t, "permanent_error", dlMsgs[0].Values["dl_reason"])
}

func TestConsumer_LiveRedis_DeadLetter_MaxRetries(t *testing.T) {
	client := redisClient(t)

	producer := redisstream.NewProducer(client)
	ctx := context.Background()
	stream := fmt.Sprintf("test:deadletter:max:%d", time.Now().UnixNano())
	group := "test-group-maxretry"
	dlStream := stream + ".dead"

	msg, err := redisstream.NewMessage("retry.test", "payload")
	require.NoError(t, err)
	_, err = producer.Publish(ctx, stream, msg)
	require.NoError(t, err)

	var attempts atomic.Int32

	consumer, err := redisstream.NewConsumer(client, group,
		redisstream.WithConsumerLogger(slog.Default()),
		redisstream.WithBlockDuration(500*time.Millisecond),
		redisstream.WithMaxRetries(3),
		redisstream.WithClaimMinIdle(500*time.Millisecond),
		redisstream.WithClaimInterval(500*time.Millisecond),
	)
	require.NoError(t, err)

	consumeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	go consumer.Consume(consumeCtx, stream, func(_ context.Context, _ redisstream.Message) error {
		attempts.Add(1)
		return errors.New("transient failure")
	})

	// Poll until the message appears in the dead-letter stream.
	require.Eventually(t, func() bool {
		dlMsgs, err := client.XRange(ctx, dlStream, "-", "+").Result()
		return err == nil && len(dlMsgs) > 0
	}, 15*time.Second, 200*time.Millisecond)

	cancel()

	// Verify the message was dead-lettered with the right reason.
	dlMsgs, err := client.XRange(ctx, dlStream, "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, dlMsgs, 1)
	assert.Equal(t, "max_retries_exceeded", dlMsgs[0].Values["dl_reason"])
	assert.GreaterOrEqual(t, attempts.Load(), int32(3))
}
