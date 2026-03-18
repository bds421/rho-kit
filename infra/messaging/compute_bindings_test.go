package messaging_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
)

// --- ComputeBindings ---

func TestComputeBindings_Valid_DirectWithRetry(t *testing.T) {
	spec := messaging.BindingSpec{
		Exchange:     "orders",
		ExchangeType: messaging.ExchangeDirect,
		Queue:        "orders.created",
		RoutingKey:   "order.created",
		Retry: &messaging.RetryPolicy{
			MaxRetries: 3,
			Delay:      5 * time.Second,
		},
	}

	bindings, err := messaging.ComputeBindings(spec)
	require.NoError(t, err)
	require.Len(t, bindings, 1)

	b := bindings[0]
	assert.Equal(t, "orders", b.Exchange)
	assert.Equal(t, "orders.created", b.Queue)
	assert.Equal(t, "order.created", b.RoutingKey)

	// Naming convention: <exchange>.retry, <queue>.retry, <exchange>.dead, <queue>.dead
	assert.Equal(t, "orders.retry", b.RetryExchange)
	assert.Equal(t, "orders.created.retry", b.RetryQueue)
	assert.Equal(t, "orders.dead", b.DeadExchange)
	assert.Equal(t, "orders.created.dead", b.DeadQueue)
}

func TestComputeBindings_Valid_FanoutNoRetry(t *testing.T) {
	spec := messaging.BindingSpec{
		Exchange:     "notifications",
		ExchangeType: messaging.ExchangeFanout,
		Queue:        "notifications.email",
		RoutingKey:   "",
	}

	bindings, err := messaging.ComputeBindings(spec)
	require.NoError(t, err)
	require.Len(t, bindings, 1)

	b := bindings[0]
	assert.Equal(t, "notifications", b.Exchange)
	assert.Equal(t, "notifications.email", b.Queue)
}

func TestComputeBindings_Valid_TopicWithRetry(t *testing.T) {
	spec := messaging.BindingSpec{
		Exchange:     "events",
		ExchangeType: messaging.ExchangeTopic,
		Queue:        "events.audit",
		RoutingKey:   "events.#",
		Retry: &messaging.RetryPolicy{
			MaxRetries: 5,
			Delay:      10 * time.Second,
		},
	}

	bindings, err := messaging.ComputeBindings(spec)
	require.NoError(t, err)
	require.Len(t, bindings, 1)

	b := bindings[0]
	assert.Equal(t, "events.retry", b.RetryExchange)
	assert.Equal(t, "events.audit.retry", b.RetryQueue)
	assert.Equal(t, "events.dead", b.DeadExchange)
	assert.Equal(t, "events.audit.dead", b.DeadQueue)
}

func TestComputeBindings_Valid_HeadersExchange(t *testing.T) {
	spec := messaging.BindingSpec{
		Exchange:     "headers.ex",
		ExchangeType: messaging.ExchangeHeaders,
		Queue:        "headers.queue",
		RoutingKey:   "",
	}

	bindings, err := messaging.ComputeBindings(spec)
	require.NoError(t, err)
	require.Len(t, bindings, 1)
}

func TestComputeBindings_MultipleSpecs(t *testing.T) {
	specs := []messaging.BindingSpec{
		{
			Exchange:     "ex1",
			ExchangeType: messaging.ExchangeDirect,
			Queue:        "q1",
			RoutingKey:   "rk1",
		},
		{
			Exchange:     "ex2",
			ExchangeType: messaging.ExchangeFanout,
			Queue:        "q2",
			RoutingKey:   "",
			Retry: &messaging.RetryPolicy{
				MaxRetries: 2,
				Delay:      time.Second,
			},
		},
	}

	bindings, err := messaging.ComputeBindings(specs...)
	require.NoError(t, err)
	require.Len(t, bindings, 2)

	assert.Equal(t, "q1", bindings[0].Queue)
	assert.Empty(t, bindings[0].RetryExchange)

	assert.Equal(t, "q2", bindings[1].Queue)
	assert.Equal(t, "ex2.retry", bindings[1].RetryExchange)
}

func TestComputeBindings_NoSpecs(t *testing.T) {
	bindings, err := messaging.ComputeBindings()
	require.NoError(t, err)
	assert.Empty(t, bindings)
}

func TestComputeBindings_OriginalSpecPreserved(t *testing.T) {
	retry := &messaging.RetryPolicy{MaxRetries: 3, Delay: time.Second}
	spec := messaging.BindingSpec{
		Exchange:     "ex",
		ExchangeType: messaging.ExchangeDirect,
		Queue:        "q",
		RoutingKey:   "rk",
		Retry:        retry,
	}

	bindings, err := messaging.ComputeBindings(spec)
	require.NoError(t, err)
	require.Len(t, bindings, 1)

	// The embedded BindingSpec must match the input.
	assert.Equal(t, spec.Exchange, bindings[0].Exchange)
	assert.Equal(t, spec.Queue, bindings[0].Queue)
	assert.Equal(t, spec.RoutingKey, bindings[0].RoutingKey)
	assert.Equal(t, spec.ExchangeType, bindings[0].ExchangeType)
	assert.Equal(t, retry, bindings[0].Retry)
}

// --- ComputeBindings validation errors ---

func TestComputeBindings_ValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		spec   messaging.BindingSpec
		errMsg string
	}{
		{
			name: "empty exchange",
			spec: messaging.BindingSpec{
				Queue:        "q",
				ExchangeType: messaging.ExchangeDirect,
				RoutingKey:   "rk",
			},
			errMsg: "exchange name must not be empty",
		},
		{
			name: "empty queue",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				ExchangeType: messaging.ExchangeDirect,
				RoutingKey:   "rk",
			},
			errMsg: "queue name must not be empty",
		},
		{
			name: "unsupported exchange type",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				Queue:        "q",
				ExchangeType: "x-custom",
				RoutingKey:   "rk",
			},
			errMsg: "unsupported exchange type",
		},
		{
			name: "missing routing key for direct exchange",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				Queue:        "q",
				ExchangeType: messaging.ExchangeDirect,
				RoutingKey:   "",
			},
			errMsg: "routing key required for direct exchange",
		},
		{
			name: "missing routing key for topic exchange",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				Queue:        "q",
				ExchangeType: messaging.ExchangeTopic,
				RoutingKey:   "",
			},
			errMsg: "routing key required for topic exchange",
		},
		{
			name: "retry MaxRetries less than 1",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				Queue:        "q",
				ExchangeType: messaging.ExchangeDirect,
				RoutingKey:   "rk",
				Retry:        &messaging.RetryPolicy{MaxRetries: 0, Delay: time.Second},
			},
			errMsg: "MaxRetries must be >= 1",
		},
		{
			name: "retry Delay zero",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				Queue:        "q",
				ExchangeType: messaging.ExchangeDirect,
				RoutingKey:   "rk",
				Retry:        &messaging.RetryPolicy{MaxRetries: 1, Delay: 0},
			},
			errMsg: "Delay must be > 0",
		},
		{
			name: "retry Delay negative",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				Queue:        "q",
				ExchangeType: messaging.ExchangeDirect,
				RoutingKey:   "rk",
				Retry:        &messaging.RetryPolicy{MaxRetries: 1, Delay: -time.Second},
			},
			errMsg: "Delay must be > 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := messaging.ComputeBindings(tt.spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestComputeBindings_ValidationError_ReturnsNilBindings(t *testing.T) {
	_, err := messaging.ComputeBindings(messaging.BindingSpec{
		Queue:        "q",
		ExchangeType: messaging.ExchangeDirect,
		RoutingKey:   "rk",
		// Exchange is empty — triggers validation error
	})

	assert.Error(t, err)
}

func TestComputeBindings_FirstInvalidSpecFails(t *testing.T) {
	validSpec := messaging.BindingSpec{
		Exchange:     "ex",
		ExchangeType: messaging.ExchangeDirect,
		Queue:        "q",
		RoutingKey:   "rk",
	}
	invalidSpec := messaging.BindingSpec{
		// Missing exchange
		ExchangeType: messaging.ExchangeDirect,
		Queue:        "q2",
		RoutingKey:   "rk2",
	}

	_, err := messaging.ComputeBindings(validSpec, invalidSpec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exchange name must not be empty")
}
