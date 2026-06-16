package redisbackend

import (
	"context"
	"log/slog"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	stream "github.com/bds421/rho-kit/data/stream/redisstream/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestNewConsumer_PanicsOnNilConsumer(t *testing.T) {
	assert.Panics(t, func() {
		NewConsumer(nil, slog.Default())
	})
}

func TestConsumer_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		consumer *Consumer
	}{
		{"nil", nil},
		{"zero", &Consumer{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding := messaging.Binding{BindingSpec: messaging.BindingSpec{Exchange: "stream"}}
			err := tc.consumer.Consume(ctx, binding, func(context.Context, messaging.Delivery) error { return nil })
			assert.ErrorIs(t, err, messaging.ErrInvalidConsumer)

			err = tc.consumer.ConsumeOnce(ctx, binding, func(context.Context, messaging.Delivery) error { return nil })
			assert.ErrorIs(t, err, messaging.ErrInvalidConsumer)
		})
	}
}

func TestConsumer_SecondInvocationReturnsErrorNotPanic(t *testing.T) {
	cases := []struct {
		name  string
		first func(*Consumer, context.Context, messaging.Binding, messaging.Handler) error
		again func(*Consumer, context.Context, messaging.Binding, messaging.Handler) error
	}{
		{
			name:  "Consume then Consume",
			first: (*Consumer).Consume,
			again: (*Consumer).Consume,
		},
		{
			name:  "ConsumeOnce then ConsumeOnce",
			first: (*Consumer).ConsumeOnce,
			again: (*Consumer).ConsumeOnce,
		},
		{
			name:  "Consume then ConsumeOnce",
			first: (*Consumer).Consume,
			again: (*Consumer).ConsumeOnce,
		},
		{
			name:  "ConsumeOnce then Consume",
			first: (*Consumer).ConsumeOnce,
			again: (*Consumer).Consume,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:0"})
			t.Cleanup(func() { _ = client.Close() })

			streamConsumer, err := stream.NewConsumer(client, "group")
			require.NoError(t, err)
			consumer := NewConsumer(streamConsumer, slog.Default())

			binding := messaging.Binding{BindingSpec: messaging.BindingSpec{Exchange: "test.stream"}}
			handler := func(context.Context, messaging.Delivery) error { return nil }

			// A cancelled context lets the first invocation return immediately
			// (after the underlying single-shot consumer is consumed) without
			// touching the network. It returns ctx.Err() as a normal shutdown.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			require.NotPanics(t, func() {
				err = tc.first(consumer, ctx, binding, handler)
			})
			require.ErrorIs(t, err, context.Canceled)

			// The second invocation must surface a clear error rather than
			// panicking from deep inside redisstream's single-shot guard.
			require.NotPanics(t, func() {
				err = tc.again(consumer, ctx, binding, handler)
			})
			assert.ErrorIs(t, err, messaging.ErrInvalidConsumer)
		})
	}
}

func TestConsumer_BindingGroupMismatchDoesNotReflectNames(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })

	streamConsumer, err := stream.NewConsumer(client, "wrapped-secret-token-group")
	require.NoError(t, err)
	consumer := NewConsumer(streamConsumer, slog.Default())

	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			Exchange:      "test.stream",
			ConsumerGroup: "binding-secret-token-group",
		},
	}
	err = consumer.Consume(context.Background(), binding, func(context.Context, messaging.Delivery) error { return nil })

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "wrapped-secret-token-group")
	assert.NotContains(t, err.Error(), "binding-secret-token-group")
	assert.Contains(t, err.Error(), "Binding.ConsumerGroup does not match wrapped consumer group")
}
