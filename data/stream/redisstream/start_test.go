package redisstream

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/redis/v2"
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

// TestStartConsumers_MalformedStreamName verifies that a stream name which
// passes the empty check but fails redis.ValidateName (whitespace, control
// characters, or over the length limit) is rejected up front with a
// BindingError, instead of passing validation and then panicking inside the
// spawned consumer goroutine (which would recover the panic and trigger a
// graceful self-shutdown rather than failing startup).
func TestStartConsumers_MalformedStreamName(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	consumer, err := NewConsumer(client, "test-group")
	require.NoError(t, err)

	cases := []struct {
		name   string
		stream string
	}{
		{"whitespace", "bad stream"},
		{"control char", "bad\x00stream"},
		{"too long", strings.Repeat("a", 257)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bindings := []Binding{
				{Stream: tc.stream, Handler: func(context.Context, Message) error { return nil }},
			}

			var wg sync.WaitGroup
			shutdownCalled := false
			err := StartConsumers(
				context.Background(),
				consumer,
				bindings,
				&wg,
				slog.Default(),
				func() { shutdownCalled = true },
			)

			require.Error(t, err)
			var bindingErr *redis.BindingError
			assert.ErrorAs(t, err, &bindingErr, "malformed stream name must yield a BindingError")
			assert.Equal(t, 0, bindingErr.Index)

			// No consumer goroutine should have been launched, so nothing to
			// wait on and shutdownFn must not have fired.
			wg.Wait()
			assert.False(t, shutdownCalled, "startup must fail, not self-shutdown")
		})
	}
}
