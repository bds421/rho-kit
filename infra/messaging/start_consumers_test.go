package messaging

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- StartConsumers validation ---

func TestStartConsumers_MissingHandler_ReturnsError(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "order.created", Queue: "orders"}},
		{BindingSpec: BindingSpec{RoutingKey: "user.updated", Queue: "users"}},
	}
	handlers := map[string]Handler{
		"order.created": func(_ context.Context, _ Delivery) error { return nil },
		// "user.updated" handler is missing
	}

	err := StartConsumers(context.Background(), nil, bindings, handlers, nil, discardLogger(), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "user.updated")
	assert.Contains(t, err.Error(), "no handlers registered")
}

func TestStartConsumers_MultipleMissingHandlers(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "a.event", Queue: "qa"}},
		{BindingSpec: BindingSpec{RoutingKey: "b.event", Queue: "qb"}},
		{BindingSpec: BindingSpec{RoutingKey: "c.event", Queue: "qc"}},
	}
	handlers := map[string]Handler{
		"a.event": func(_ context.Context, _ Delivery) error { return nil },
	}

	err := StartConsumers(context.Background(), nil, bindings, handlers, nil, discardLogger(), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "b.event")
	assert.Contains(t, err.Error(), "c.event")
}

func TestStartConsumers_AllHandlersPresent_NoError(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "order.created", Queue: "orders"}},
	}
	handlers := map[string]Handler{
		"order.created": func(_ context.Context, _ Delivery) error { return nil },
	}

	// We need a real WaitGroup and Consumer for the goroutine launch,
	// but the goroutine will call Consumer.Consume which needs a real connection.
	// Instead, we test that validation passes (no error) with a nil consumer.
	// The goroutine will panic, but we verify the shutdown function is called.
	var shutdownCalled bool
	var wg sync.WaitGroup

	err := StartConsumers(
		context.Background(),
		nil, // nil consumer will panic in the goroutine
		bindings,
		handlers,
		&wg,
		discardLogger(),
		func() { shutdownCalled = true },
	)

	require.NoError(t, err, "validation should pass when all handlers are present")

	// Wait for the goroutine to complete (it will panic and recover).
	wg.Wait()

	assert.True(t, shutdownCalled, "shutdown function should be called after panic recovery")
}

func TestStartConsumers_PanicRecovery_NilShutdownFn(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "order.created", Queue: "orders"}},
	}
	handlers := map[string]Handler{
		"order.created": func(_ context.Context, _ Delivery) error { return nil },
	}

	var wg sync.WaitGroup

	// Nil shutdownFn should not panic on recovery.
	err := StartConsumers(
		context.Background(),
		nil,
		bindings,
		handlers,
		&wg,
		discardLogger(),
		nil, // nil shutdownFn
	)

	require.NoError(t, err)
	wg.Wait() // goroutine panics, recovers, and does not call nil shutdownFn
}

func TestStartConsumers_EmptyBindings_NoError(t *testing.T) {
	handlers := map[string]Handler{
		"order.created": func(_ context.Context, _ Delivery) error { return nil },
	}

	err := StartConsumers(context.Background(), nil, nil, handlers, nil, discardLogger(), nil)

	assert.NoError(t, err)
}
