package amqpbackend

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestRetryQueueArgs_DeadLettersBackToOriginatingQueueViaDefaultExchange(t *testing.T) {
	tests := []struct {
		name string
		spec messaging.BindingSpec
	}{
		{
			name: "fanout binding (empty routing key)",
			spec: messaging.BindingSpec{
				Exchange:      "events",
				ExchangeType:  messaging.ExchangeFanout,
				ConsumerGroup: "notifications.email",
				RoutingKey:    "",
				Retry:         &messaging.RetryPolicy{MaxRetries: 3, Delay: 10 * time.Second},
			},
		},
		{
			name: "topic binding (pattern routing key)",
			spec: messaging.BindingSpec{
				Exchange:      "events",
				ExchangeType:  messaging.ExchangeTopic,
				ConsumerGroup: "orders.audit",
				RoutingKey:    "orders.*",
				Retry:         &messaging.RetryPolicy{MaxRetries: 2, Delay: 5 * time.Second},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := retryQueueArgs(tt.spec)

			// Route via the default exchange so the expired retry message is
			// delivered ONLY to the originating queue — never re-fanned-out to
			// sibling consumer groups, and never carrying a binding pattern as
			// its routing key.
			assert.Equal(t, "", args["x-dead-letter-exchange"],
				"retry queue must dead-letter through the default exchange, not the original exchange")
			assert.Equal(t, tt.spec.ConsumerGroup, args["x-dead-letter-routing-key"],
				"default-exchange routing key must be the originating queue name")

			// TTL stays correct.
			assert.Equal(t, int64(tt.spec.Retry.Delay/time.Millisecond), args["x-message-ttl"])
		})
	}
}

func TestDeclareExchanges_ValidatesBeforeOpeningChannel(t *testing.T) {
	err := DeclareExchanges(noopConnector{}, messaging.ExchangeSpec{
		Exchange:     "events\nprod",
		ExchangeType: messaging.ExchangeDirect,
	})

	assert.ErrorIs(t, err, messaging.ErrInvalidRoute)
	assert.NotContains(t, err.Error(), "noop")
}

func TestDeclareExchanges_RejectsUnsupportedExchangeTypeBeforeOpeningChannel(t *testing.T) {
	err := DeclareExchanges(noopConnector{}, messaging.ExchangeSpec{
		Exchange:     "events",
		ExchangeType: "custom",
	})

	assert.Contains(t, err.Error(), "unsupported exchange type")
	assert.NotContains(t, err.Error(), "custom")
	assert.NotContains(t, err.Error(), "noop")
}

func TestDeclareAll_DoesNotMutateCallerSpecsBeforeOpeningChannel(t *testing.T) {
	specs := []messaging.BindingSpec{{
		Exchange:      "events",
		ExchangeType:  messaging.ExchangeDirect,
		ConsumerGroup: "q",
		RoutingKey:    "rk",
	}}

	_, err := DeclareAll(noopConnector{}, specs...)
	assert.Error(t, err)
	assert.Nil(t, specs[0].Retry)
}
