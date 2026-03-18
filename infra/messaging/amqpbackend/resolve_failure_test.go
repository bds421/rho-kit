package amqpbackend

import (
	"log/slog"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
)

// --- deadExchangeName ---

func TestDeadExchangeName(t *testing.T) {
	assert.Equal(t, "events.dead", messaging.DeadExchangeName("events"))
	assert.Equal(t, "orders.service.dead", messaging.DeadExchangeName("orders.service"))
	assert.Equal(t, ".dead", messaging.DeadExchangeName(""))
}

// --- resolveFailure ---

func retryBinding(maxRetries int) messaging.Binding {
	return messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			Exchange:     "test.exchange",
			ExchangeType: messaging.ExchangeDirect,
			Queue:        "test.queue",
			RoutingKey:   "test.key",
			Retry: &messaging.RetryPolicy{
				MaxRetries: maxRetries,
				Delay:      100 * time.Millisecond,
			},
		},
		RetryExchange: "test.exchange.retry",
		RetryQueue:    "test.queue.retry",
		DeadExchange:  "test.exchange.dead",
		DeadQueue:     "test.queue.dead",
	}
}

func deliveryWithXDeathCount(queue string, count int64) amqp.Delivery {
	if count == 0 {
		return amqp.Delivery{}
	}
	return amqp.Delivery{
		Headers: amqp.Table{
			"x-death": []any{
				amqp.Table{
					"queue":  queue,
					"reason": "rejected",
					"count":  count,
				},
			},
		},
	}
}

func TestResolveFailure_NoRetry(t *testing.T) {
	b := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			Exchange:     "test.exchange",
			ExchangeType: messaging.ExchangeDirect,
			Queue:        "test.queue",
			RoutingKey:   "test.key",
			Retry:        nil,
		},
	}
	delivery := amqp.Delivery{}

	action, retryCount := resolveFailure(delivery, b)

	assert.Equal(t, actionDiscard, action)
	assert.Equal(t, 0, retryCount)
}

func TestResolveFailure_UnderMax(t *testing.T) {
	b := retryBinding(3)
	// xDeathCount = 1 < MaxRetries (3) → actionRetry
	delivery := deliveryWithXDeathCount("test.queue", 1)

	action, retryCount := resolveFailure(delivery, b)

	assert.Equal(t, actionRetry, action)
	assert.Equal(t, 1, retryCount)
}

func TestResolveFailure_AtMax(t *testing.T) {
	b := retryBinding(3)
	// xDeathCount = 3 >= MaxRetries (3) but < safetyLimit (9) → actionDeadLetter
	delivery := deliveryWithXDeathCount("test.queue", 3)

	action, retryCount := resolveFailure(delivery, b)

	assert.Equal(t, actionDeadLetter, action)
	assert.Equal(t, 3, retryCount)
}

func TestResolveFailure_ExceedsSafetyLimit(t *testing.T) {
	b := retryBinding(3)
	// safetyLimit = 3 * 3 = 9; xDeathCount = 9 >= safetyLimit → actionForceDiscard
	delivery := deliveryWithXDeathCount("test.queue", 9)

	action, retryCount := resolveFailure(delivery, b)

	assert.Equal(t, actionForceDiscard, action)
	assert.Equal(t, 9, retryCount)
}

func TestResolveFailure_ZeroCount_IsRetry(t *testing.T) {
	b := retryBinding(3)
	// First failure: xDeathCount = 0 < MaxRetries → actionRetry
	delivery := amqp.Delivery{}

	action, retryCount := resolveFailure(delivery, b)

	assert.Equal(t, actionRetry, action)
	assert.Equal(t, 0, retryCount)
}

func TestResolveFailure_OneBelowSafetyLimit(t *testing.T) {
	b := retryBinding(3)
	// safetyLimit = 9; xDeathCount = 8 → actionDeadLetter (>= MaxRetries but < safetyLimit)
	delivery := deliveryWithXDeathCount("test.queue", 8)

	action, retryCount := resolveFailure(delivery, b)

	assert.Equal(t, actionDeadLetter, action)
	assert.Equal(t, 8, retryCount)
}

func TestResolveFailure_SafetyMultiplierApplied(t *testing.T) {
	// safetyMaxBounceMultiplier = 3, so safety limit = MaxRetries * 3
	b := retryBinding(5)
	// safetyLimit = 15; xDeathCount = 15 → actionForceDiscard
	delivery := deliveryWithXDeathCount("test.queue", 15)

	action, _ := resolveFailure(delivery, b)

	assert.Equal(t, actionForceDiscard, action)
}

// --- NewConsumer defaults ---

func TestNewConsumer_Defaults(t *testing.T) {
	c := NewConsumer(nil, nil, slog.Default())

	require.NotNil(t, c)
	assert.Equal(t, defaultPrefetch, c.prefetch, "default prefetch should be %d", defaultPrefetch)
	assert.Nil(t, c.conn)
	assert.Nil(t, c.publisher)
	assert.NotNil(t, c.logger)
}

func TestNewConsumer_StoresConnectionAndPublisher(t *testing.T) {
	mockConn := &Connection{}
	c := NewConsumer(mockConn, nil, slog.Default())

	assert.Equal(t, mockConn, c.conn)
}

// --- WithPrefetch ---

func TestWithPrefetch_OverridesDefault(t *testing.T) {
	c := NewConsumer(nil, nil, slog.Default(), WithPrefetch(50))

	assert.Equal(t, 50, c.prefetch)
}

func TestWithPrefetch_One(t *testing.T) {
	c := NewConsumer(nil, nil, slog.Default(), WithPrefetch(1))

	assert.Equal(t, 1, c.prefetch)
}

func TestWithPrefetch_HighValue(t *testing.T) {
	c := NewConsumer(nil, nil, slog.Default(), WithPrefetch(1000))

	assert.Equal(t, 1000, c.prefetch)
}

// --- WithHooks ---

func TestWithHooks_SetsHooks(t *testing.T) {
	var retryCalled bool
	var deadLetterCalled bool
	var discardCalled bool

	hooks := ConsumerHooks{
		OnRetry:      func(msgID, msgType, queue string, retryCount int) { retryCalled = true },
		OnDeadLetter: func(msgID, msgType, queue string, retryCount int) { deadLetterCalled = true },
		OnDiscard:    func(msgID, msgType, queue string) { discardCalled = true },
	}

	c := NewConsumer(nil, nil, slog.Default(), WithHooks(hooks))

	require.NotNil(t, c.hooks.OnRetry)
	require.NotNil(t, c.hooks.OnDeadLetter)
	require.NotNil(t, c.hooks.OnDiscard)

	c.hooks.OnRetry("id", "type", "queue", 1)
	assert.True(t, retryCalled)

	c.hooks.OnDeadLetter("id", "type", "queue", 3)
	assert.True(t, deadLetterCalled)

	c.hooks.OnDiscard("id", "type", "queue")
	assert.True(t, discardCalled)
}

func TestWithHooks_NilHooksAreAccepted(t *testing.T) {
	hooks := ConsumerHooks{
		OnRetry:      nil,
		OnDeadLetter: nil,
		OnDiscard:    nil,
	}

	c := NewConsumer(nil, nil, slog.Default(), WithHooks(hooks))

	assert.Nil(t, c.hooks.OnRetry)
	assert.Nil(t, c.hooks.OnDeadLetter)
	assert.Nil(t, c.hooks.OnDiscard)
}

func TestWithHooks_MultipleOptionsApplied(t *testing.T) {
	var discardCalled bool
	hooks := ConsumerHooks{
		OnDiscard: func(msgID, msgType, queue string) { discardCalled = true },
	}

	c := NewConsumer(nil, nil, slog.Default(),
		WithPrefetch(25),
		WithHooks(hooks),
	)

	assert.Equal(t, 25, c.prefetch)
	require.NotNil(t, c.hooks.OnDiscard)
	c.hooks.OnDiscard("id", "type", "queue")
	assert.True(t, discardCalled)
}
