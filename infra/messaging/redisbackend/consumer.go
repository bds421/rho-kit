package redisbackend

import (
	"context"
	"log/slog"

	stream "github.com/bds421/rho-kit/data/stream/redisstream/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// Consumer wraps a stream.Consumer to satisfy messaging.MessageConsumer.
// The Binding.Exchange maps to the Redis stream name and Binding.Queue maps
// to the consumer group.
type Consumer struct {
	consumer *stream.Consumer
	logger   *slog.Logger
}

// NewConsumer creates a Consumer backed by the given StreamConsumer.
// Panics if consumer is nil — the wrapper dereferences it on every
// Consume, so accepting nil here would only defer the crash to the
// first delivery. A nil logger is normalized to [slog.Default].
func NewConsumer(consumer *stream.Consumer, logger *slog.Logger) *Consumer {
	if consumer == nil {
		panic("redisbackend: NewConsumer requires a non-nil *stream.Consumer")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Consumer{consumer: consumer, logger: logger}
}

// Consume blocks until ctx is cancelled, dispatching messages to handler.
// It delegates to the StreamConsumer's built-in retry and dead-letter logic.
// The Binding.Exchange is used as the stream name.
func (c *Consumer) Consume(ctx context.Context, b messaging.Binding, handler messaging.Handler) error {
	streamName := b.Exchange
	c.consumer.Consume(ctx, streamName, func(ctx context.Context, sm stream.Message) error {
		d := toDelivery(sm, streamName)
		return handler(ctx, d)
	})
	return ctx.Err()
}

// ConsumeOnce reads from the stream until context is cancelled or an error
// occurs. For Redis Streams, this delegates to Consume since the underlying
// StreamConsumer already handles reconnection internally.
func (c *Consumer) ConsumeOnce(ctx context.Context, b messaging.Binding, handler messaging.Handler) error {
	return c.Consume(ctx, b, handler)
}
