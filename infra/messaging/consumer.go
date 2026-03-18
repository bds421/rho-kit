package messaging

import "context"

// Handler processes a received Delivery. Return nil to acknowledge,
// or an error to nack (backend handles retry/dead-letter if configured).
type Handler func(ctx context.Context, d Delivery) error

// MessageConsumer consumes messages from a broker. Backend implementations
// (amqpbackend.Consumer, redisbackend.Consumer) satisfy this interface.
type MessageConsumer interface {
	// Consume blocks until ctx is cancelled, dispatching messages to handler.
	// Resilient: reconnects automatically on transport errors.
	// Returns nil when ctx is cancelled (normal shutdown), or an error if
	// reconnection has been permanently abandoned (e.g., max retries exceeded,
	// configuration error).
	Consume(ctx context.Context, b Binding, handler Handler) error

	// ConsumeOnce reads until the context is cancelled or the transport
	// connection drops. Callers typically wrap this in a retry loop.
	ConsumeOnce(ctx context.Context, b Binding, handler Handler) error
}
