//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	"github.com/bds421/rho-kit/core/v2/apperror"
	kafkabackend "github.com/bds421/rho-kit/infra/messaging/kafkabackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func createTopic(t *testing.T, brokers []string, topic string) {
	t.Helper()
	client := &kafka.Client{
		Addr:    kafka.TCP(brokers...),
		Timeout: 30 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.CreateTopics(ctx, &kafka.CreateTopicsRequest{
		Topics: []kafka.TopicConfig{{
			Topic:             topic,
			NumPartitions:     1,
			ReplicationFactor: 1,
		}},
	})
	require.NoError(t, err)
	for name, terr := range resp.Errors {
		require.NoErrorf(t, terr, "create topic %s", name)
	}
	// Brief settle so a fresh topic's metadata propagates to the
	// publisher/consumer cache.
	time.Sleep(500 * time.Millisecond)
}

func startKafka(t *testing.T) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.5.0",
		tckafka.WithClusterID("rho-kit-test"),
		// Auto-create topics so the test does not have to manage broker
		// admin separately. Production code must declare topics
		// explicitly; this is a test convenience only.
		testcontainers.WithEnv(map[string]string{
			"KAFKA_AUTO_CREATE_TOPICS_ENABLE": "true",
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	brokers, err := c.Brokers(ctx)
	require.NoError(t, err)
	return brokers
}

func TestPublishConsume_RoundTrip(t *testing.T) {
	brokers := startKafka(t)
	const topic = "rho-kit-test-events"
	const group = "rho-kit-test-group"
	createTopic(t, brokers, topic)

	pub, err := kafkabackend.NewPublisherWithConfig(kafkabackend.Config{
		Brokers:       brokers,
		AllowInsecure: true,
	})
	require.NoError(t, err)
	defer func() { _ = pub.Close() }()
	sub, err := kafkabackend.NewSubscriberWithConfig(kafkabackend.Config{
		Brokers:       brokers,
		AllowInsecure: true,
	}, group, []string{topic},
		kafkabackend.WithSubscriberLogger(slog.Default()),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	got := make(chan messaging.Delivery, 8)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sub.Consume(ctx, messaging.Binding{
			BindingSpec: messaging.BindingSpec{Exchange: topic, Queue: group},
		}, func(_ context.Context, d messaging.Delivery) error {
			got <- d
			return nil
		})
	}()

	// Allow consumer-group join + initial offset fetch before publishing.
	time.Sleep(2 * time.Second)

	for _, name := range []string{"alice", "bob", "carol"} {
		msg, err := messaging.NewMessage("user.created", map[string]string{"name": name})
		require.NoError(t, err)
		require.NoError(t, pub.Publish(ctx, topic, "user.created", msg))
	}

	for i := 0; i < 3; i++ {
		select {
		case d := <-got:
			assert.Equal(t, topic, d.Exchange)
			assert.Equal(t, "user.created", d.RoutingKey)
			assert.Equal(t, "user.created", d.Message.Type)
		case <-ctx.Done():
			t.Fatalf("timeout waiting for delivery %d", i)
		}
	}

	cancel()
	wg.Wait()
}

func TestSubscriber_PermanentErrorAdvancesOffset(t *testing.T) {
	brokers := startKafka(t)
	const topic = "rho-kit-test-permanent"
	const group = "rho-kit-test-permanent-group"
	createTopic(t, brokers, topic)

	pub, err := kafkabackend.NewPublisherWithConfig(kafkabackend.Config{
		Brokers:       brokers,
		AllowInsecure: true,
	})
	require.NoError(t, err)
	defer func() { _ = pub.Close() }()
	sub, err := kafkabackend.NewSubscriberWithConfig(kafkabackend.Config{
		Brokers:       brokers,
		AllowInsecure: true,
	}, group, []string{topic})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	calls := make(chan messaging.Delivery, 4)
	var mu sync.Mutex
	var attempts int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sub.Consume(ctx, messaging.Binding{
			BindingSpec: messaging.BindingSpec{Exchange: topic, Queue: group},
		}, func(_ context.Context, d messaging.Delivery) error {
			mu.Lock()
			attempts++
			mu.Unlock()
			calls <- d
			if d.Message.Type == "poison.pill" {
				return apperror.NewPermanent("permanent failure")
			}
			return nil
		})
	}()

	time.Sleep(2 * time.Second)

	poison, err := messaging.NewMessage("poison.pill", map[string]string{"k": "v"})
	require.NoError(t, err)
	require.NoError(t, pub.Publish(ctx, topic, "poison.pill", poison))

	follow, err := messaging.NewMessage("follow.up", map[string]string{"k": "v"})
	require.NoError(t, err)
	require.NoError(t, pub.Publish(ctx, topic, "follow.up", follow))

	// Drain two distinct deliveries: poison then follow-up. If the poison
	// pill were not committed (advance offset), the consumer would retry
	// the poison message forever and never reach follow.up.
	var sawPoison, sawFollow bool
	for !sawPoison || !sawFollow {
		select {
		case d := <-calls:
			switch d.Message.Type {
			case "poison.pill":
				sawPoison = true
			case "follow.up":
				sawFollow = true
			}
		case <-ctx.Done():
			t.Fatalf("timeout waiting for follow-up after poison pill (saw poison=%v follow=%v attempts=%d)", sawPoison, sawFollow, attempts)
		}
	}

	cancel()
	wg.Wait()
}
