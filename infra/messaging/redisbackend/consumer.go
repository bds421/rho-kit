package redisbackend

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	stream "github.com/bds421/rho-kit/data/stream/redisstream/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// Consumer wraps a stream.Consumer to satisfy messaging.Consumer.
// Binding.Exchange maps to the Redis stream name.
//
// FR-064 [MED]: Binding.ConsumerGroup is interpreted as the *expected*
// consumer-group name. The wrapper does NOT switch groups per
// binding because the underlying *stream.Consumer is constructed
// with a fixed group. If Binding.ConsumerGroup is non-empty the wrapper
// validates it equals the wrapped consumer's group and returns an
// error otherwise — pre-fix the field was silently ignored, so a
// service binding multiple queues to one Consumer would route every
// delivery through the constructor-time group regardless of
// Binding.ConsumerGroup. Construct one Consumer per (stream, group) pair.
type Consumer struct {
	consumer *stream.Consumer
	logger   *slog.Logger
	// consumed records whether Consume/ConsumeOnce has already been
	// invoked. The wrapped *stream.Consumer is single-shot and panics
	// across the package boundary on a second Consume; this flag lets
	// the wrapper return a clear error instead. Construct one Consumer
	// per (stream, group) pair.
	consumed atomic.Bool
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

func (c *Consumer) ready() error {
	if c == nil || c.consumer == nil || c.logger == nil {
		return messaging.ErrInvalidConsumer
	}
	return nil
}

// Consume blocks until ctx is cancelled, dispatching messages to handler.
// It delegates to the StreamConsumer's built-in retry and dead-letter logic.
// The Binding.Exchange is used as the stream name; Binding.ConsumerGroup, when
// non-empty, must match the wrapped consumer's group (audit FR-064).
func (c *Consumer) Consume(ctx context.Context, b messaging.Binding, handler messaging.Handler) error {
	if err := c.ready(); err != nil {
		return err
	}
	if handler == nil {
		return messaging.ErrInvalidConsumer
	}
	if err := messaging.ValidateExchangeName(b.Exchange); err != nil {
		return err
	}
	if b.ConsumerGroup != "" && b.ConsumerGroup != c.consumer.Group() {
		return fmt.Errorf("redisbackend: Binding.ConsumerGroup does not match wrapped consumer group (FR-064): construct a separate Consumer per group")
	}
	// The wrapped *stream.Consumer is single-shot: a second Consume panics
	// across the package boundary. Guard here so re-invoking Consume or
	// ConsumeOnce (which delegates to Consume) returns a clear error
	// instead. Construct a separate Consumer per (stream, group) pair.
	if !c.consumed.CompareAndSwap(false, true) {
		return fmt.Errorf("redisbackend: Consumer already consumed; construct a separate Consumer per stream: %w", messaging.ErrInvalidConsumer)
	}
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
