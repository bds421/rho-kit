//go:build integration

package amqpbackend_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kittestamqp "github.com/bds421/rho-kit/testing/kittest/v2/amqp"
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func amqpTestName(t *testing.T, suffix string) string {
	t.Helper()

	name := strings.NewReplacer("/", ".", " ", ".", ":", ".", "#", ".", "*", ".").Replace(t.Name())
	return "test." + name + suffix
}

func setupConsumerTest(t *testing.T) (*amqpbackend.Connection, *amqpbackend.Publisher, messaging.Binding) {
	t.Helper()

	url := kittestamqp.Start(t)

	conn, err := dialLocalRabbitMQ(t, url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Stop(context.Background()) })

	db, err := amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
		Exchange:     amqpTestName(t, ".exchange"),
		ExchangeType: messaging.ExchangeDirect,
		ConsumerGroup:        amqpTestName(t, ".queue"),
		RoutingKey:   amqpTestName(t, ".key"),
		WithoutRetry: true,
	})
	require.NoError(t, err)

	pub := amqpbackend.NewPublisher(conn, slog.Default())

	return conn, pub, db
}

func publishRaw(t *testing.T, conn *amqpbackend.Connection, exchange, routingKey string, body []byte) {
	t.Helper()

	ch, err := conn.Channel()
	require.NoError(t, err)
	defer ch.Close()

	err = ch.PublishWithContext(context.Background(), exchange, routingKey, false, false,
		amqp.Publishing{ContentType: "application/json", Body: body},
	)
	require.NoError(t, err)
}

func TestConsumeOnce_HandlerDispatch(t *testing.T) {
	conn, pub, db := setupConsumerTest(t)

	msg, err := messaging.NewMessage("test.event", map[string]string{"key": "value"})
	require.NoError(t, err)
	require.NoError(t, pub.Publish(context.Background(), db.Exchange, db.RoutingKey, msg))

	var received messaging.Message
	var mu sync.Mutex
	done := make(chan struct{})

	handler := func(_ context.Context, d messaging.Delivery) error {
		mu.Lock()
		received = d.Message
		mu.Unlock()
		close(done)
		return nil
	}

	consumer := amqpbackend.NewConsumer(conn, nil, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		_ = consumer.ConsumeOnce(ctx, db, handler)
	}()

	select {
	case <-done:
		mu.Lock()
		assert.Equal(t, msg.ID, received.ID)
		assert.Equal(t, msg.Type, received.Type)
		mu.Unlock()
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}
}

func TestConsumeOnce_AckOnSuccess(t *testing.T) {
	conn, pub, db := setupConsumerTest(t)

	msg, err := messaging.NewMessage("test.event", "payload")
	require.NoError(t, err)
	require.NoError(t, pub.Publish(context.Background(), db.Exchange, db.RoutingKey, msg))

	done := make(chan struct{})
	handler := func(_ context.Context, _ messaging.Delivery) error {
		close(done)
		return nil
	}

	consumer := amqpbackend.NewConsumer(conn, nil, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		_ = consumer.ConsumeOnce(ctx, db, handler)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for ack handler")
	}
	cancel()
	time.Sleep(100 * time.Millisecond) // allow ack to complete

	ch, err := conn.Channel()
	require.NoError(t, err)
	defer ch.Close()

	_, ok, err := ch.Get(db.ConsumerGroup, true)
	require.NoError(t, err)
	assert.False(t, ok, "expected queue to be empty after successful ack")
}

func TestConsumeOnce_DLXRetryFlow(t *testing.T) {
	url := kittestamqp.Start(t)
	conn, err := dialLocalRabbitMQ(t, url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Stop(context.Background()) })

	spec := messaging.BindingSpec{
		Exchange:     "test.retry",
		ExchangeType: messaging.ExchangeDirect,
		ConsumerGroup:        "test.retry.queue",
		RoutingKey:   "test.retry.key",
		Retry: &messaging.RetryPolicy{
			MaxRetries: 2,
			Delay:      100 * time.Millisecond,
		},
	}
	declared, err := amqpbackend.DeclareAll(conn, spec)
	require.NoError(t, err)

	pub := amqpbackend.NewPublisher(conn, slog.Default())
	msg, err := messaging.NewMessage("test.event", "payload")
	require.NoError(t, err)
	require.NoError(t, pub.Publish(context.Background(), "test.retry", "test.retry.key", msg))

	var callCount atomic.Int32
	done := make(chan struct{})

	handler := func(_ context.Context, _ messaging.Delivery) error {
		n := callCount.Add(1)
		if n < 3 {
			return errors.New("temporary failure")
		}
		close(done)
		return nil
	}

	consumer := amqpbackend.NewConsumer(conn, pub, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	go func() {
		_ = consumer.ConsumeOnce(ctx, declared[0], handler)
	}()

	select {
	case <-done:
		assert.Equal(t, int32(3), callCount.Load(), "expected handler to be called 3 times (1 initial + 2 retries)")
	case <-ctx.Done():
		t.Fatalf("timed out waiting for DLX retry, handler was called %d times", callCount.Load())
	}
}

func TestConsumeOnce_MaxRetriesExceeded_GoesToDeadQueue(t *testing.T) {
	url := kittestamqp.Start(t)
	conn, err := dialLocalRabbitMQ(t, url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Stop(context.Background()) })

	spec := messaging.BindingSpec{
		Exchange:     "test.maxretry",
		ExchangeType: messaging.ExchangeDirect,
		ConsumerGroup:        "test.maxretry.queue",
		RoutingKey:   "test.maxretry.key",
		Retry: &messaging.RetryPolicy{
			MaxRetries: 1,
			Delay:      100 * time.Millisecond,
		},
	}
	declared, err := amqpbackend.DeclareAll(conn, spec)
	require.NoError(t, err)

	pub := amqpbackend.NewPublisher(conn, slog.Default())
	msg, err := messaging.NewMessage("test.event", "payload")
	require.NoError(t, err)
	require.NoError(t, pub.Publish(context.Background(), "test.maxretry", "test.maxretry.key", msg))

	var callCount atomic.Int32
	maxRetriesReached := make(chan struct{})

	handler := func(_ context.Context, _ messaging.Delivery) error {
		n := callCount.Add(1)
		if n >= 2 {
			select {
			case <-maxRetriesReached:
			default:
				close(maxRetriesReached)
			}
		}
		return errors.New("always fails")
	}

	consumer := amqpbackend.NewConsumer(conn, pub, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	go func() {
		_ = consumer.ConsumeOnce(ctx, declared[0], handler)
	}()

	select {
	case <-maxRetriesReached:
	case <-ctx.Done():
		t.Fatal("timed out waiting for max retries to be reached")
	}

	// Poll the dead queue until the message arrives, instead of a fixed sleep.
	require.Eventually(t, func() bool {
		ch, err := conn.Channel()
		if err != nil {
			return false
		}
		defer ch.Close()

		deadMsg, ok, err := ch.Get("test.maxretry.queue.dead", false)
		if err != nil || !ok {
			return false
		}

		var deadContent messaging.Message
		if json.Unmarshal(deadMsg.Body, &deadContent) != nil {
			return false
		}
		assert.Equal(t, msg.ID, deadContent.ID)
		_ = deadMsg.Ack(false)
		return true
	}, 10*time.Second, 50*time.Millisecond, "expected message in dead queue")
}

func TestConsumeOnce_NoRetryConfig_JustNacks(t *testing.T) {
	conn, pub, db := setupConsumerTest(t)

	msg, err := messaging.NewMessage("test.event", "payload")
	require.NoError(t, err)
	require.NoError(t, pub.Publish(context.Background(), db.Exchange, db.RoutingKey, msg))

	var callCount atomic.Int32
	firstCallDone := make(chan struct{})

	handler := func(_ context.Context, _ messaging.Delivery) error {
		callCount.Add(1)
		select {
		case <-firstCallDone:
		default:
			close(firstCallDone)
		}
		return errors.New("always fails")
	}

	// No retry — binding has Retry: nil, so messages are discarded on error.
	consumer := amqpbackend.NewConsumer(conn, nil, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		_ = consumer.ConsumeOnce(ctx, db, handler)
	}()

	select {
	case <-firstCallDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for no-retry handler")
	}

	// Without DLX on the queue, nack(false, false) discards the message.
	// Verify handler is never called a second time.
	require.Never(t, func() bool {
		return callCount.Load() > 1
	}, 500*time.Millisecond, 50*time.Millisecond, "handler should not be called again (no DLX, message discarded)")

	cancel()
}

func TestConsumeOnce_MalformedMessageDiscard(t *testing.T) {
	conn, _, db := setupConsumerTest(t)

	publishRaw(t, conn, db.Exchange, db.RoutingKey, []byte("not-json"))

	validMsg, err := messaging.NewMessage("test.event", "valid")
	require.NoError(t, err)
	body, _ := json.Marshal(validMsg)
	publishRaw(t, conn, db.Exchange, db.RoutingKey, body)

	done := make(chan struct{})
	handler := func(_ context.Context, d messaging.Delivery) error {
		assert.Equal(t, validMsg.ID, d.Message.ID)
		close(done)
		return nil
	}

	consumer := amqpbackend.NewConsumer(conn, nil, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		_ = consumer.ConsumeOnce(ctx, db, handler)
	}()

	select {
	case <-done:
		// malformed message was discarded, valid message was processed
	case <-ctx.Done():
		t.Fatal("timed out — malformed message may have blocked the consumer")
	}
}

func TestConsumeOnce_DeliveryMetadata(t *testing.T) {
	conn, _, db := setupConsumerTest(t)

	ch, err := conn.Channel()
	require.NoError(t, err)

	msg, err := messaging.NewMessage("test.event", "payload")
	require.NoError(t, err)
	body, _ := json.Marshal(msg)

	err = ch.PublishWithContext(context.Background(), db.Exchange, db.RoutingKey, false, false,
		amqp.Publishing{
			ContentType:   "application/json",
			Body:          body,
			ReplyTo:       "reply.queue",
			CorrelationId: "corr-123",
		},
	)
	require.NoError(t, err)
	ch.Close()

	done := make(chan struct{})
	var received messaging.Delivery

	handler := func(_ context.Context, d messaging.Delivery) error {
		received = d
		close(done)
		return nil
	}

	consumer := amqpbackend.NewConsumer(conn, nil, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		_ = consumer.ConsumeOnce(ctx, db, handler)
	}()

	select {
	case <-done:
		assert.Equal(t, "reply.queue", received.ReplyTo)
		assert.Equal(t, "corr-123", received.CorrelationID)
		assert.Equal(t, db.Exchange, received.Exchange)
		assert.Equal(t, db.RoutingKey, received.RoutingKey)
	case <-ctx.Done():
		t.Fatal("timed out waiting for message with metadata")
	}
}

func TestConsumeOnce_RequiresPublisher(t *testing.T) {
	url := kittestamqp.Start(t)
	conn, err := dialLocalRabbitMQ(t, url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Stop(context.Background()) })

	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			Exchange:     "test.pub-required",
			ExchangeType: messaging.ExchangeDirect,
			ConsumerGroup:        "test.pub-required.queue",
			RoutingKey:   "test.key",
			Retry:        &messaging.RetryPolicy{MaxRetries: 1, Delay: time.Second},
		},
		DeadExchange: "test.pub-required.dead",
	}

	// nil publisher with retry binding → error
	consumer := amqpbackend.NewConsumer(conn, nil, slog.Default())
	err = consumer.ConsumeOnce(context.Background(), binding, func(_ context.Context, _ messaging.Delivery) error {
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publisher")
}
