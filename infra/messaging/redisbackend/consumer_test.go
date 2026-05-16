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

func TestConsumer_BindingGroupMismatchDoesNotReflectNames(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })

	streamConsumer, err := stream.NewConsumer(client, "wrapped-secret-token-group")
	require.NoError(t, err)
	consumer := NewConsumer(streamConsumer, slog.Default())

	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			Exchange: "test.stream",
			ConsumerGroup:    "binding-secret-token-group",
		},
	}
	err = consumer.Consume(context.Background(), binding, func(context.Context, messaging.Delivery) error { return nil })

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "wrapped-secret-token-group")
	assert.NotContains(t, err.Error(), "binding-secret-token-group")
	assert.Contains(t, err.Error(), "Binding.ConsumerGroup does not match wrapped consumer group")
}
