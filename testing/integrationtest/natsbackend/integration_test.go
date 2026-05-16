//go:build integration

package natsbackend_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/nats"

	"github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func startNATS(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := nats.Run(ctx, "nats:2.11-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	url, err := c.ConnectionString(ctx)
	require.NoError(t, err)
	return url
}

func TestPublishConsume_RoundTrip(t *testing.T) {
	url := startNATS(t)
	conn, err := natsbackend.Connect(context.Background(), natsbackend.Config{URL: url, AllowInsecure: true})
	require.NoError(t, err)
	defer conn.Stop(context.Background())

	require.NoError(t, conn.EnsureStream(context.Background(), natsbackend.StreamConfig{
		Name:        "EVENTS",
		Subjects:    []string{"events.>"},
		Retention:   jetstream.LimitsPolicy,
		StorageType: jetstream.MemoryStorage,
	}))

	pub := natsbackend.NewPublisher(conn)
	cons := natsbackend.NewConsumer(conn, natsbackend.ConsumerConfig{
		Stream:        "EVENTS",
		Durable:       "test-consumer",
		FilterSubject: "events.>",
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got := make(chan messaging.Delivery, 4)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = cons.Consume(ctx, func(_ context.Context, d messaging.Delivery) error {
			got <- d
			return nil
		})
	}()

	// Allow consumer to attach.
	time.Sleep(200 * time.Millisecond)

	for _, name := range []string{"alice", "bob", "carol"} {
		msg, err := messaging.NewMessage("user.created", map[string]string{"name": name})
		require.NoError(t, err)
		require.NoError(t, pub.Publish(ctx, "events", "user.created", msg))
	}

	for i := 0; i < 3; i++ {
		select {
		case d := <-got:
			assert.Equal(t, "events", d.Exchange)
			assert.Equal(t, "user.created", d.RoutingKey)
			assert.Equal(t, "user.created", d.Message.Type)
		case <-ctx.Done():
			t.Fatalf("timeout waiting for delivery %d", i)
		}
	}

	cancel()
	wg.Wait()
}

func TestConsumer_NackRedelivers(t *testing.T) {
	url := startNATS(t)
	conn, err := natsbackend.Connect(context.Background(), natsbackend.Config{URL: url, AllowInsecure: true})
	require.NoError(t, err)
	defer conn.Stop(context.Background())

	require.NoError(t, conn.EnsureStream(context.Background(), natsbackend.StreamConfig{
		Name:        "RETRY",
		Subjects:    []string{"retry.>"},
		Retention:   jetstream.LimitsPolicy,
		StorageType: jetstream.MemoryStorage,
	}))

	pub := natsbackend.NewPublisher(conn)
	// Short AckWait so the redelivery happens within the test window.
	cons := natsbackend.NewConsumer(conn, natsbackend.ConsumerConfig{
		Stream:  "RETRY",
		Durable: "retry-consumer",
		AckWait: time.Second,
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deliveries := make(chan bool, 4)
	var attempts int
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = cons.Consume(ctx, func(_ context.Context, d messaging.Delivery) error {
			mu.Lock()
			attempts++
			n := attempts
			mu.Unlock()
			deliveries <- d.Redelivered
			if n == 1 {
				return assertErr // first attempt nacks
			}
			return nil
		})
	}()

	time.Sleep(200 * time.Millisecond)

	msg, err := messaging.NewMessage("retry.flaky", map[string]string{"k": "v"})
	require.NoError(t, err)
	require.NoError(t, pub.Publish(ctx, "retry", "flaky", msg))

	first := <-deliveries
	assert.False(t, first, "first delivery should not be marked Redelivered")
	second := <-deliveries
	assert.True(t, second, "second delivery (after nack) must be marked Redelivered")

	cancel()
	wg.Wait()
}

// assertErr is a sentinel returned by nack-once handlers.
var assertErr = errSentinel("nack")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
