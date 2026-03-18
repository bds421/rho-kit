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
