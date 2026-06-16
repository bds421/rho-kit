package redisqueue

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStartProcessors_EmptyQueueName(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	bindings := []Binding{
		{Queue: "", Handler: func(context.Context, Message) error { return nil }},
	}

	err := StartProcessors(context.Background(), q, bindings, &sync.WaitGroup{}, slog.Default(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "queue name must not be empty")
}

func TestStartProcessors_NilHandler(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	bindings := []Binding{
		{Queue: "test-queue", Handler: nil},
	}

	err := StartProcessors(context.Background(), q, bindings, &sync.WaitGroup{}, slog.Default(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "handler must not be nil")
}

// TestStartProcessors_DuplicateQueueName ensures a duplicate Binding.Queue is
// rejected at startup with a BindingError pointing at the second occurrence,
// rather than being launched and tripping the active-queue panic guard at
// runtime (which the per-goroutine recover would escalate to shutdownFn). No
// goroutine must be started, so wg.Wait returns immediately and shutdownFn is
// never engaged.
func TestStartProcessors_DuplicateQueueName(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })

	noop := func(context.Context, Message) error { return nil }
	bindings := []Binding{
		{Queue: "orders", Handler: noop},
		{Queue: "payments", Handler: noop},
		{Queue: "orders", Handler: noop},
	}

	var wg sync.WaitGroup
	shutdownCalled := false
	err := StartProcessors(context.Background(), q, bindings, &wg, slog.Default(), func() {
		shutdownCalled = true
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate queue name in bindings")
	assert.Contains(t, err.Error(), "binding [2]", "error must identify the duplicate's index")
	assert.NotContains(t, err.Error(), "orders", "queue name must not leak into the error string")

	// No processor goroutines were launched and shutdownFn was not engaged.
	wg.Wait()
	assert.False(t, shutdownCalled, "a config rejection must not trigger shutdownFn")
}
