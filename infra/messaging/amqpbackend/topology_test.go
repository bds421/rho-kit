//go:build integration

package amqpbackend_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend"
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/rabbitmqtest"
)

func TestDeclareTopology_Direct(t *testing.T) {
	url := rabbitmqtest.Start(t)
	conn, err := amqpbackend.Dial(url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
		Exchange:     "test.direct",
		ExchangeType: messaging.ExchangeDirect,
		Queue:        "test.direct.queue",
		RoutingKey:   "test.key",
	})
	assert.NoError(t, err)
}

func TestDeclareTopology_Fanout(t *testing.T) {
	url := rabbitmqtest.Start(t)
	conn, err := amqpbackend.Dial(url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
		Exchange:     "test.fanout",
		ExchangeType: messaging.ExchangeFanout,
		Queue:        "test.fanout.queue",
		RoutingKey:   "",
	})
	assert.NoError(t, err)
}

func TestDeclareTopology_Topic(t *testing.T) {
	url := rabbitmqtest.Start(t)
	conn, err := amqpbackend.Dial(url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
		Exchange:     "test.topic",
		ExchangeType: messaging.ExchangeTopic,
		Queue:        "test.topic.queue",
		RoutingKey:   "test.#",
	})
	assert.NoError(t, err)
}

func TestDeclareTopology_Headers(t *testing.T) {
	url := rabbitmqtest.Start(t)
	conn, err := amqpbackend.Dial(url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
		Exchange:     "test.headers",
		ExchangeType: messaging.ExchangeHeaders,
		Queue:        "test.headers.queue",
		RoutingKey:   "",
	})
	assert.NoError(t, err)
}

func TestDeclareTopology_Idempotent(t *testing.T) {
	url := rabbitmqtest.Start(t)
	conn, err := amqpbackend.Dial(url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	spec := messaging.BindingSpec{
		Exchange:     "test.idempotent",
		ExchangeType: messaging.ExchangeDirect,
		Queue:        "test.idempotent.queue",
		RoutingKey:   "test.key",
	}

	_, err = amqpbackend.DeclareTopology(conn, spec)
	assert.NoError(t, err)
	_, err = amqpbackend.DeclareTopology(conn, spec)
	assert.NoError(t, err)
}

func TestDeclareAll_RetryTopology(t *testing.T) {
	url := rabbitmqtest.Start(t)
	conn, err := amqpbackend.Dial(url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	spec := messaging.BindingSpec{
		Exchange:     "test.retrytopo",
		ExchangeType: messaging.ExchangeDirect,
		Queue:        "test.retrytopo.queue",
		RoutingKey:   "test.key",
		Retry: &messaging.RetryPolicy{
			MaxRetries: 3,
			Delay:      500 * time.Millisecond,
		},
	}

	declared, err := amqpbackend.DeclareAll(conn, spec)
	require.NoError(t, err)
	require.Len(t, declared, 1)

	db := declared[0]

	// Verify Binding has correct computed names.
	assert.Equal(t, "test.retrytopo.retry", db.RetryExchange)
	assert.Equal(t, "test.retrytopo.queue.retry", db.RetryQueue)
	assert.Equal(t, "test.retrytopo.dead", db.DeadExchange)
	assert.Equal(t, "test.retrytopo.queue.dead", db.DeadQueue)

	// Verify retry queue exists and has correct properties by publishing
	// a message to the retry exchange and confirming it arrives back at
	// the main queue after the TTL expires.
	pub := amqpbackend.NewPublisher(conn, slog.Default())
	msg, err := messaging.NewMessage("test.event", "topo-check")
	require.NoError(t, err)

	// Publish directly to retry exchange with routing key = queue name.
	// The retry queue should hold it for 500ms, then dead-letter back
	// to the main exchange with routing key "test.key".
	require.NoError(t, pub.Publish(context.Background(),
		db.RetryExchange,
		db.Queue, // routing key for retry queue binding
		msg,
	))

	// Message should NOT be in main queue yet (still in retry queue TTL).
	ch, err := conn.Channel()
	require.NoError(t, err)
	_, ok, err := ch.Get("test.retrytopo.queue", true)
	require.NoError(t, err)
	assert.False(t, ok, "message should still be in retry queue during TTL")
	ch.Close()

	// After TTL, message should arrive in the main queue.
	require.Eventually(t, func() bool {
		ch, err := conn.Channel()
		if err != nil {
			return false
		}
		defer ch.Close()

		delivery, ok, err := ch.Get("test.retrytopo.queue", true)
		if err != nil || !ok {
			return false
		}

		var received messaging.Message
		return json.Unmarshal(delivery.Body, &received) == nil && received.ID == msg.ID
	}, 5*time.Second, 100*time.Millisecond, "message should arrive in main queue after retry TTL")

	// Verify dead queue exists by publishing directly to it.
	deadMsg, err := messaging.NewMessage("test.dead", "dead-check")
	require.NoError(t, err)
	require.NoError(t, pub.Publish(context.Background(), db.DeadExchange, db.Queue, deadMsg))

	ch, err = conn.Channel()
	require.NoError(t, err)
	defer ch.Close()

	delivery, ok, err := ch.Get("test.retrytopo.queue.dead", true)
	require.NoError(t, err)
	require.True(t, ok, "dead queue should exist and contain the message")

	var receivedDead messaging.Message
	require.NoError(t, json.Unmarshal(delivery.Body, &receivedDead))
	assert.Equal(t, deadMsg.ID, receivedDead.ID)
}

func TestDeclareAll_NoRetry_EmptyDeclaredFields(t *testing.T) {
	url := rabbitmqtest.Start(t)
	conn, err := amqpbackend.Dial(url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	declared, err := amqpbackend.DeclareAll(conn, messaging.BindingSpec{
		Exchange:     "test.noretry",
		ExchangeType: messaging.ExchangeDirect,
		Queue:        "test.noretry.queue",
		RoutingKey:   "test.key",
	})
	require.NoError(t, err)
	require.Len(t, declared, 1)

	db := declared[0]
	assert.Empty(t, db.RetryExchange)
	assert.Empty(t, db.RetryQueue)
	assert.Empty(t, db.DeadExchange)
	assert.Empty(t, db.DeadQueue)
}

func TestDeclareAll_ValidationErrors(t *testing.T) {
	url := rabbitmqtest.Start(t)
	conn, err := amqpbackend.Dial(url, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	tests := []struct {
		name    string
		binding messaging.BindingSpec
		errMsg  string
	}{
		{
			name:    "empty exchange",
			binding: messaging.BindingSpec{Queue: "q", ExchangeType: messaging.ExchangeDirect},
			errMsg:  "exchange name must not be empty",
		},
		{
			name:    "empty queue",
			binding: messaging.BindingSpec{Exchange: "e", ExchangeType: messaging.ExchangeDirect},
			errMsg:  "queue name must not be empty",
		},
		{
			name:    "invalid exchange type",
			binding: messaging.BindingSpec{Exchange: "e", Queue: "q", ExchangeType: "invalid"},
			errMsg:  "unsupported exchange type",
		},
		{
			name:    "missing routing key for direct exchange",
			binding: messaging.BindingSpec{Exchange: "e", Queue: "q", ExchangeType: messaging.ExchangeDirect},
			errMsg:  "routing key required for direct exchange",
		},
		{
			name:    "missing routing key for topic exchange",
			binding: messaging.BindingSpec{Exchange: "e", Queue: "q", ExchangeType: messaging.ExchangeTopic},
			errMsg:  "routing key required for topic exchange",
		},
		{
			name: "retry max retries < 1",
			binding: messaging.BindingSpec{
				Exchange: "e", Queue: "q", ExchangeType: messaging.ExchangeDirect, RoutingKey: "rk",
				Retry: &messaging.RetryPolicy{MaxRetries: 0, Delay: time.Second},
			},
			errMsg: "MaxRetries must be >= 1",
		},
		{
			name: "retry delay <= 0",
			binding: messaging.BindingSpec{
				Exchange: "e", Queue: "q", ExchangeType: messaging.ExchangeDirect, RoutingKey: "rk",
				Retry: &messaging.RetryPolicy{MaxRetries: 1, Delay: 0},
			},
			errMsg: "Delay must be > 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := amqpbackend.DeclareAll(conn, tt.binding)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}
