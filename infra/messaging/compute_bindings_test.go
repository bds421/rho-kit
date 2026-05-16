package messaging_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// --- ComputeBindings ---

func TestComputeBindings_Valid_DirectWithRetry(t *testing.T) {
	spec := messaging.BindingSpec{
		Exchange:     "orders",
		ExchangeType: messaging.ExchangeDirect,
		ConsumerGroup:        "orders.created",
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
	assert.Equal(t, "orders.created", b.ConsumerGroup)
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
		ConsumerGroup:        "notifications.email",
		RoutingKey:   "",
	}

	bindings, err := messaging.ComputeBindings(spec)
	require.NoError(t, err)
	require.Len(t, bindings, 1)

	b := bindings[0]
	assert.Equal(t, "notifications", b.Exchange)
	assert.Equal(t, "notifications.email", b.ConsumerGroup)
}

func TestComputeBindings_Valid_TopicWithRetry(t *testing.T) {
	spec := messaging.BindingSpec{
		Exchange:     "events",
		ExchangeType: messaging.ExchangeTopic,
		ConsumerGroup:        "events.audit",
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
		ConsumerGroup:        "headers.queue",
		RoutingKey:   "",
	}

	bindings, err := messaging.ComputeBindings(spec)
	require.NoError(t, err)
	require.Len(t, bindings, 1)
}

func TestComputeBindings_MultipleSpecs(t *testing.T) {
	specs := []messaging.BindingSpec{
		{
			// Explicit fire-and-forget binding — no retry topology.
			Exchange:     "ex1",
			ExchangeType: messaging.ExchangeDirect,
			ConsumerGroup:        "q1",
			RoutingKey:   "rk1",
			WithoutRetry: true,
		},
		{
			Exchange:     "ex2",
			ExchangeType: messaging.ExchangeFanout,
			ConsumerGroup:        "q2",
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

	assert.Equal(t, "q1", bindings[0].ConsumerGroup)
	assert.Empty(t, bindings[0].RetryExchange, "WithoutRetry binding has no retry topology")

	assert.Equal(t, "q2", bindings[1].ConsumerGroup)
	assert.Equal(t, "ex2.retry", bindings[1].RetryExchange)
}

func TestComputeBindings_NilRetryGetsDefault(t *testing.T) {
	specs := []messaging.BindingSpec{
		{
			Exchange:     "ex",
			ExchangeType: messaging.ExchangeDirect,
			ConsumerGroup:        "q",
			RoutingKey:   "rk",
			// no Retry, no WithoutRetry — kit applies DefaultRetryPolicy.
		},
	}

	bindings, err := messaging.ComputeBindings(specs...)
	require.NoError(t, err)
	require.Len(t, bindings, 1)

	require.NotNil(t, bindings[0].Retry, "default retry must be applied")
	assert.Equal(t, 3, bindings[0].Retry.MaxRetries)
	assert.Equal(t, 10*time.Second, bindings[0].Retry.Delay)
	assert.Equal(t, "ex.retry", bindings[0].RetryExchange)
}

func TestComputeBindings_DefaultRetryDoesNotMutateInput(t *testing.T) {
	specs := []messaging.BindingSpec{
		{
			Exchange:     "ex",
			ExchangeType: messaging.ExchangeDirect,
			ConsumerGroup:        "q",
			RoutingKey:   "rk",
		},
	}

	bindings, err := messaging.ComputeBindings(specs...)
	require.NoError(t, err)
	require.Len(t, bindings, 1)

	assert.Nil(t, specs[0].Retry)
	require.NotNil(t, bindings[0].Retry)
	assert.Equal(t, 3, bindings[0].Retry.MaxRetries)
}

func TestNormalizeBindingSpecs_WarningDoesNotReflectQueueName(t *testing.T) {
	specs := []messaging.BindingSpec{{
		Exchange:     "ex",
		ExchangeType: messaging.ExchangeDirect,
		ConsumerGroup:        "secret-token",
		RoutingKey:   "rk",
	}}

	warnings := messaging.NormalizeBindingSpecs(specs)
	require.Len(t, warnings, 1)
	assert.NotContains(t, strings.ToLower(warnings[0]), "secret-token")
}

func TestValidateBindingSpecs_RetryAndWithoutRetryConflict(t *testing.T) {
	specs := []messaging.BindingSpec{
		{
			Exchange:     "ex",
			ExchangeType: messaging.ExchangeDirect,
			ConsumerGroup:        "q",
			RoutingKey:   "rk",
			Retry:        &messaging.RetryPolicy{MaxRetries: 3, Delay: time.Second},
			WithoutRetry: true, // conflict
		},
	}
	err := messaging.ValidateBindingSpecs(specs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
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
		ConsumerGroup:        "q",
		RoutingKey:   "rk",
		Retry:        retry,
	}

	bindings, err := messaging.ComputeBindings(spec)
	require.NoError(t, err)
	require.Len(t, bindings, 1)

	// The embedded BindingSpec must match the input.
	assert.Equal(t, spec.Exchange, bindings[0].Exchange)
	assert.Equal(t, spec.ConsumerGroup, bindings[0].ConsumerGroup)
	assert.Equal(t, spec.RoutingKey, bindings[0].RoutingKey)
	assert.Equal(t, spec.ExchangeType, bindings[0].ExchangeType)
	require.NotNil(t, bindings[0].Retry)
	assert.NotSame(t, retry, bindings[0].Retry)
	assert.Equal(t, retry, bindings[0].Retry)

	retry.MaxRetries = 99
	retry.Delay = time.Minute
	assert.Equal(t, 3, bindings[0].Retry.MaxRetries)
	assert.Equal(t, time.Second, bindings[0].Retry.Delay)
}

func TestFindBinding_ClonesRetryPolicy(t *testing.T) {
	retry := &messaging.RetryPolicy{MaxRetries: 3, Delay: time.Second}
	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			Exchange:   "ex",
			ConsumerGroup:      "q",
			RoutingKey: "rk",
			Retry:      retry,
		},
	}

	found, err := messaging.FindBinding([]messaging.Binding{binding}, "rk")
	require.NoError(t, err)
	require.NotNil(t, found.Retry)
	assert.NotSame(t, retry, found.Retry)

	retry.MaxRetries = 99
	assert.Equal(t, 3, found.Retry.MaxRetries)
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
				ConsumerGroup:        "q",
				ExchangeType: messaging.ExchangeDirect,
				RoutingKey:   "rk",
			},
			errMsg: "exchange name must not be empty",
		},
		{
			name: "empty consumer group",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				ExchangeType: messaging.ExchangeDirect,
				RoutingKey:   "rk",
			},
			errMsg: "consumer group must not be empty",
		},
		{
			name: "unsupported exchange type",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				ConsumerGroup:        "q",
				ExchangeType: "x-custom",
				RoutingKey:   "rk",
			},
			errMsg: "unsupported exchange type",
		},
		{
			name: "missing routing key for direct exchange",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				ConsumerGroup:        "q",
				ExchangeType: messaging.ExchangeDirect,
				RoutingKey:   "",
			},
			errMsg: "routing key required for direct exchange",
		},
		{
			name: "missing routing key for topic exchange",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				ConsumerGroup:        "q",
				ExchangeType: messaging.ExchangeTopic,
				RoutingKey:   "",
			},
			errMsg: "routing key required for topic exchange",
		},
		{
			name: "retry MaxRetries less than 1",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				ConsumerGroup:        "q",
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
				ConsumerGroup:        "q",
				ExchangeType: messaging.ExchangeDirect,
				RoutingKey:   "rk",
				Retry:        &messaging.RetryPolicy{MaxRetries: 1, Delay: 0},
			},
			errMsg: "Delay must be >= 1ms",
		},
		{
			name: "retry Delay negative",
			spec: messaging.BindingSpec{
				Exchange:     "ex",
				ConsumerGroup:        "q",
				ExchangeType: messaging.ExchangeDirect,
				RoutingKey:   "rk",
				Retry:        &messaging.RetryPolicy{MaxRetries: 1, Delay: -time.Second},
			},
			errMsg: "Delay must be >= 1ms",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := messaging.ComputeBindings(tt.spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
			assert.NotContains(t, err.Error(), "-1s")
			assert.NotContains(t, err.Error(), "0s")
		})
	}
}

func TestValidateBindingSpecs_DoesNotReflectBindingMetadata(t *testing.T) {
	tests := map[string][]messaging.BindingSpec{
		"unsupported exchange type": {{
			Exchange:     "events",
			ExchangeType: "secret-token",
			ConsumerGroup:        "queue",
			RoutingKey:   "rk",
		}},
		"missing routing key": {{
			Exchange:     "secret-token",
			ExchangeType: messaging.ExchangeDirect,
			ConsumerGroup:        "queue",
		}},
		"retry conflict": {{
			Exchange:     "events",
			ExchangeType: messaging.ExchangeDirect,
			ConsumerGroup:        "secret-token",
			RoutingKey:   "rk",
			Retry:        &messaging.RetryPolicy{MaxRetries: 1, Delay: time.Second},
			WithoutRetry: true,
		}},
		"retry max retries": {{
			Exchange:     "events",
			ExchangeType: messaging.ExchangeDirect,
			ConsumerGroup:        "secret-token",
			RoutingKey:   "rk",
			Retry:        &messaging.RetryPolicy{MaxRetries: 0, Delay: time.Second},
		}},
		"retry delay": {{
			Exchange:     "events",
			ExchangeType: messaging.ExchangeDirect,
			ConsumerGroup:        "secret-token",
			RoutingKey:   "rk",
			Retry:        &messaging.RetryPolicy{MaxRetries: 1},
		}},
	}

	for name, specs := range tests {
		t.Run(name, func(t *testing.T) {
			err := messaging.ValidateBindingSpecs(specs)
			require.Error(t, err)
			assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
		})
	}
}

func TestFindBinding_DoesNotReflectRoutingKey(t *testing.T) {
	_, err := messaging.FindBinding([]messaging.Binding{{
		BindingSpec: messaging.BindingSpec{RoutingKey: "known"},
	}}, "secret-token")

	require.Error(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
}

func TestComputeBindings_ValidationError_ReturnsNilBindings(t *testing.T) {
	_, err := messaging.ComputeBindings(messaging.BindingSpec{
		ConsumerGroup:        "q",
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
		ConsumerGroup:        "q",
		RoutingKey:   "rk",
	}
	invalidSpec := messaging.BindingSpec{
		// Missing exchange
		ExchangeType: messaging.ExchangeDirect,
		ConsumerGroup:        "q2",
		RoutingKey:   "rk2",
	}

	_, err := messaging.ComputeBindings(validSpec, invalidSpec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exchange name must not be empty")
}
