package redisstream

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartConsumers_EmptyStreamName(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	consumer, err := NewConsumer(client, "test-group")
	require.NoError(t, err)

	bindings := []Binding{
		{Stream: "", Handler: func(context.Context, Message) error { return nil }},
	}

	err = StartConsumers(context.Background(), consumer, bindings, &sync.WaitGroup{}, slog.Default(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stream name must not be empty")
}

func TestStartConsumers_NilHandler(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	consumer, err := NewConsumer(client, "test-group")
	require.NoError(t, err)

	bindings := []Binding{
		{Stream: "test-stream", Handler: nil},
	}

	err = StartConsumers(context.Background(), consumer, bindings, &sync.WaitGroup{}, slog.Default(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "handler must not be nil")
}
