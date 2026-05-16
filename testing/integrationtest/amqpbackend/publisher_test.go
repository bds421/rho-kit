//go:build integration

package amqpbackend_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kittestamqp "github.com/bds421/rho-kit/testing/kittest/v2/amqp"
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func setupPublisher(t *testing.T) (*amqpbackend.Publisher, *amqpbackend.Connection) {
	t.Helper()

	url := kittestamqp.Start(t)
	logger := slog.Default()

	conn, err := dialLocalRabbitMQ(t, url, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Stop(context.Background()) })

	_, err = amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
		Exchange:     "test.publish",
		ExchangeType: messaging.ExchangeDirect,
		ConsumerGroup:        "test.publish.queue",
		RoutingKey:   "test.key",
	})
	require.NoError(t, err)

	return amqpbackend.NewPublisher(conn, logger), conn
}

func TestPublish_Confirmed(t *testing.T) {
	pub, _ := setupPublisher(t)

	msg, err := messaging.NewMessage("test.event", map[string]string{"hello": "world"})
	require.NoError(t, err)

	err = pub.Publish(context.Background(), "test.publish", "test.key", msg)
	assert.NoError(t, err)
}

func TestPublish_PersistentDelivery(t *testing.T) {
	pub, conn := setupPublisher(t)

	msg, err := messaging.NewMessage("test.event", "payload")
	require.NoError(t, err)

	err = pub.Publish(context.Background(), "test.publish", "test.key", msg)
	require.NoError(t, err)

	ch, err := conn.Channel()
	require.NoError(t, err)
	defer ch.Close()

	delivery, ok, err := ch.Get("test.publish.queue", true)
	require.NoError(t, err)
	require.True(t, ok, "expected a message in the queue")
	assert.Equal(t, uint8(2), delivery.DeliveryMode, "expected persistent delivery mode")
}

func TestPublish_ContextCancellation(t *testing.T) {
	pub, _ := setupPublisher(t)

	msg, err := messaging.NewMessage("test.event", "payload")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond) // ensure context expires

	err = pub.Publish(ctx, "test.publish", "test.key", msg)
	assert.Error(t, err)
}
