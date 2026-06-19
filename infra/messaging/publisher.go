package messaging

import (
	"context"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// ErrInvalidPublisher is returned when a publisher method is invoked on
// a nil or otherwise uninitialized publisher implementation. Typed as
// an [apperror.UnavailableError] so HTTP/gRPC adapters surface it as
// 503/Unavailable rather than a generic 500.
var ErrInvalidPublisher = apperror.NewUnavailable("messaging: publisher is not initialized")

// ErrInvalidPublishContext is returned when a publish call receives a
// nil context. Canceled or expired contexts are returned as their
// standard context error so callers can use errors.Is(err,
// context.Canceled).
var ErrInvalidPublishContext = apperror.NewValidation("messaging: publish context is nil")

// ErrBufferFull is returned by [BufferedPublisher.Publish] when the
// in-memory buffer is at capacity and the message is dropped (load
// shedding). Callers that want to react programmatically — back off,
// retry later, or surface back-pressure — match on it with
// errors.Is(err, ErrBufferFull). Typed as an [apperror.UnavailableError]
// so HTTP/gRPC adapters surface it as 503/Unavailable rather than a
// generic 500.
var ErrBufferFull = apperror.NewUnavailable("messaging: buffered publisher buffer full, message dropped")

// Publisher is the transport-agnostic interface for publishing messages.
// Backend implementations (amqpbackend.Publisher, redisbackend.Publisher)
// satisfy this interface. The BufferedPublisher also implements it,
// adding buffered at-least-once delivery on top of any underlying
// Publisher.
type Publisher interface {
	Publish(ctx context.Context, exchange, routingKey string, msg Message) error
}

// ValidatePublishContext rejects nil and already-canceled publish contexts
// before a backend can enqueue, persist, or partially send a message.
func ValidatePublishContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidPublishContext
	}
	return ctx.Err()
}
