//go:build integration

package redisbackend

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	stream "github.com/bds421/rho-kit/data/stream/redisstream/v2"
	"github.com/bds421/rho-kit/infra/messaging/redisbackend/v2"
	"github.com/bds421/rho-kit/infra/redis/redistest/v2"
	"github.com/bds421/rho-kit/infra/redis/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func redisClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	url := redistest.Start(t)
	opts, err := goredis.ParseURL(url)
	require.NoError(t, err)
	conn, err := redis.Connect(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Cleanup(func() { redistest.FlushDB(t) })
	return conn.Client()
}

func uniqueStream(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s:%d", prefix, time.Now().UnixNano())
}

// Publish round-trips through a real Redis Stream: a Consumer attached to
// the same stream/group receives the published payload.
func TestPublisherConsumer_Roundtrip(t *testing.T) {
	client := redisClient(t)
	streamName := uniqueStream(t, "events")
	group := "g1"

	prod := stream.NewProducer(client)
	pub := redisbackend.NewPublisher(prod)

	cons, err := stream.NewConsumer(client, group)
	require.NoError(t, err)
	c := redisbackend.NewConsumer(cons, slog.Default())

	msg, err := messaging.NewMessage("user.created", map[string]string{"id": "42"})
	require.NoError(t, err)

	require.NoError(t, pub.Publish(context.Background(), streamName, "users.create", msg))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	var received atomic.Value

	go func() {
		_ = c.Consume(ctx, messaging.Binding{
			BindingSpec: messaging.BindingSpec{Exchange: streamName, ConsumerGroup: group, WithoutRetry: true},
		}, func(_ context.Context, d messaging.Delivery) error {
			received.Store(d)
			wg.Done()
			cancel()
			return nil
		})
	}()

	wg.Wait()

	d, ok := received.Load().(messaging.Delivery)
	require.True(t, ok, "Consume must hand a Delivery to the handler")
	assert.Equal(t, "user.created", d.Message.Type)
	// The routing key is carried in the message headers per redisbackend's
	// wire convention.
	assert.NotEmpty(t, d.Message.Headers)
}

// WithMaxMessageBytes refuses payloads above the configured cap before the
// stream Publish call ever runs.
func TestPublisher_WithMaxMessageBytesEnforcesCap(t *testing.T) {
	client := redisClient(t)
	streamName := uniqueStream(t, "tiny")
	prod := stream.NewProducer(client)

	// 16-byte cap.
	pub := redisbackend.NewPublisher(prod, redisbackend.WithMaxMessageBytes(16))

	msg, err := messaging.NewMessage("x", map[string]string{"k": "this is more than sixteen bytes for sure"})
	require.NoError(t, err)

	err = pub.Publish(context.Background(), streamName, "rk", msg)
	require.Error(t, err, "publish must fail with payload above the cap")
	assert.ErrorIs(t, err, messaging.ErrMessageTooLarge)
}

// Consumer.Consume returns an error if Binding.ConsumerGroup doesn't match the
// wrapped consumer's group (audit FR-064).
func TestConsumer_BindingQueueMismatch(t *testing.T) {
	client := redisClient(t)
	streamName := uniqueStream(t, "fr064")

	cons, err := stream.NewConsumer(client, "g-actual")
	require.NoError(t, err)
	c := redisbackend.NewConsumer(cons, slog.Default())

	err = c.Consume(context.Background(), messaging.Binding{
		BindingSpec: messaging.BindingSpec{Exchange: streamName, ConsumerGroup: "g-other", WithoutRetry: true},
	}, func(_ context.Context, _ messaging.Delivery) error { return nil })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Binding.ConsumerGroup does not match wrapped consumer group")
}
