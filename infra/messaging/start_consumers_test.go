package messaging

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeConsumer struct {
	err error
}

func (f fakeConsumer) Consume(context.Context, Binding, Handler) error {
	return f.err
}

func (f fakeConsumer) ConsumeOnce(context.Context, Binding, Handler) error {
	return f.err
}

// --- StartConsumers validation ---

func TestStartConsumers_MissingHandler_ReturnsError(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "order.created", ConsumerGroup: "orders"}},
		{BindingSpec: BindingSpec{RoutingKey: "user.updated", ConsumerGroup: "users"}},
	}
	handlers := map[string]Handler{
		"order.created": func(_ context.Context, _ Delivery) error { return nil },
		// "user.updated" handler is missing
	}

	var wg sync.WaitGroup
	err := StartConsumers(context.Background(), fakeConsumer{}, bindings, handlers, &wg, discardLogger(), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no handlers registered")
	assert.Contains(t, err.Error(), "count=1")
	assert.Contains(t, err.Error(), "user.updated")
}

func TestStartConsumers_MultipleMissingHandlers(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "a.event", ConsumerGroup: "qa"}},
		{BindingSpec: BindingSpec{RoutingKey: "b.event", ConsumerGroup: "qb"}},
		{BindingSpec: BindingSpec{RoutingKey: "c.event", ConsumerGroup: "qc"}},
	}
	handlers := map[string]Handler{
		"a.event": func(_ context.Context, _ Delivery) error { return nil },
	}

	var wg sync.WaitGroup
	err := StartConsumers(context.Background(), fakeConsumer{}, bindings, handlers, &wg, discardLogger(), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "count=2")
	assert.Contains(t, err.Error(), "b.event")
	assert.Contains(t, err.Error(), "c.event")
}

func TestStartConsumers_NilHandler_ReturnsError(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "order.created", ConsumerGroup: "orders"}},
	}
	handlers := map[string]Handler{
		"order.created": nil,
	}

	var wg sync.WaitGroup
	err := StartConsumers(context.Background(), fakeConsumer{}, bindings, handlers, &wg, discardLogger(), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil handlers")
	assert.Contains(t, err.Error(), "count=1")
	assert.Contains(t, err.Error(), "order.created")
}

func TestStartConsumers_NilConsumer_ReturnsError(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "order.created", ConsumerGroup: "orders"}},
	}
	handlers := map[string]Handler{
		"order.created": func(_ context.Context, _ Delivery) error { return nil },
	}

	var wg sync.WaitGroup
	err := StartConsumers(context.Background(), nil, bindings, handlers, &wg, discardLogger(), nil)

	assert.ErrorIs(t, err, ErrInvalidConsumer)
}

func TestStartConsumers_NilWaitGroup_ReturnsError(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "order.created", ConsumerGroup: "orders"}},
	}
	handlers := map[string]Handler{
		"order.created": func(_ context.Context, _ Delivery) error { return nil },
	}

	err := StartConsumers(context.Background(), fakeConsumer{}, bindings, handlers, nil, discardLogger(), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "WaitGroup")
}

func TestStartConsumers_AllHandlersPresent_NoError(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "order.created", ConsumerGroup: "orders"}},
	}
	handlers := map[string]Handler{
		"order.created": func(_ context.Context, _ Delivery) error { return nil },
	}

	var shutdownCalled bool
	var wg sync.WaitGroup

	err := StartConsumers(
		context.Background(),
		fakeConsumer{},
		bindings,
		handlers,
		&wg,
		discardLogger(),
		func() { shutdownCalled = true },
	)

	require.NoError(t, err, "validation should pass when all handlers are present")

	wg.Wait()

	assert.False(t, shutdownCalled, "shutdown function should not be called for a clean consumer return")
}

func TestStartConsumers_PanicRecovery_NilShutdownFn(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "order.created", ConsumerGroup: "orders"}},
	}
	handlers := map[string]Handler{
		"order.created": func(_ context.Context, _ Delivery) error { return nil },
	}

	var wg sync.WaitGroup

	// Nil shutdownFn should not panic on recovery.
	err := StartConsumers(
		context.Background(),
		panicConsumer{},
		bindings,
		handlers,
		&wg,
		discardLogger(),
		nil, // nil shutdownFn
	)

	require.NoError(t, err)
	wg.Wait() // goroutine panics, recovers, and does not call nil shutdownFn
}

type panicConsumer struct{}

func (panicConsumer) Consume(context.Context, Binding, Handler) error {
	panic("boom")
}

func (panicConsumer) ConsumeOnce(context.Context, Binding, Handler) error {
	panic("boom")
}

func TestStartConsumers_ConsumeErrorCallsShutdown(t *testing.T) {
	bindings := []Binding{
		{BindingSpec: BindingSpec{RoutingKey: "order.created", ConsumerGroup: "orders"}},
	}
	handlers := map[string]Handler{
		"order.created": func(_ context.Context, _ Delivery) error { return nil },
	}

	var shutdownCalled atomic.Bool
	var wg sync.WaitGroup
	err := StartConsumers(
		context.Background(),
		fakeConsumer{err: errors.New("consumer failed")},
		bindings,
		handlers,
		&wg,
		discardLogger(),
		func() { shutdownCalled.Store(true) },
	)

	require.NoError(t, err)
	wg.Wait()
	assert.True(t, shutdownCalled.Load(), "shutdown function should be called after live consumer failure")
}

func TestStartConsumers_EmptyBindings_NoError(t *testing.T) {
	handlers := map[string]Handler{
		"order.created": func(_ context.Context, _ Delivery) error { return nil },
	}

	err := StartConsumers(context.Background(), nil, nil, handlers, nil, discardLogger(), nil)

	assert.NoError(t, err)
}
