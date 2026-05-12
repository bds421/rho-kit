package messaging

import (
	"context"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// ErrInvalidConsumer is returned when a consumer method is invoked on
// a nil or otherwise uninitialized consumer implementation. Typed as an
// [apperror.UnavailableError] so HTTP/gRPC adapters surface it as
// 503/Unavailable rather than a generic 500.
var ErrInvalidConsumer = apperror.NewUnavailable("messaging: consumer is not initialized")

// Handler processes a received Delivery. Return nil to acknowledge,
// or an error to nack (backend handles retry/dead-letter if configured).
type Handler func(ctx context.Context, d Delivery) error

// Consumer consumes messages from a broker. Backend implementations
// (amqpbackend.Consumer, redisbackend.Consumer) satisfy this interface.
type Consumer interface {
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
