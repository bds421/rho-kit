package outbox

import (
	"context"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// MessagingPublisher adapts any kit messaging.Publisher (amqpbackend,
// kafkabackend, natsbackend, redisbackend, BufferedPublisher, or a
// service-defined implementation) into an outbox.Publisher.
//
// Wave 156 closes the gap left by wave 149: the [Multiplex] dispatcher
// could only route to outbox.Publisher implementations, but every
// kit messaging backend implements the wider messaging.Publisher
// interface that also carries an exchange and routing-key. This
// adapter performs the trivial conversion (Entry.Topic → exchange,
// Entry.RoutingKey → routing-key, Entry payload + headers → messaging.Message)
// so services no longer reinvent the bridge per backend.
//
// One generic adapter suffices because every backend's Publisher
// has the same signature; backend-specific concerns (publisher
// confirms on AMQP, key partitioning on Kafka) live inside the
// messaging.Publisher implementation, not in this conversion.
type MessagingPublisher struct {
	publisher messaging.Publisher
}

// NewMessagingPublisher wraps a messaging.Publisher as an outbox.Publisher.
// Panics if publisher is nil — same fail-fast convention as the
// underlying backends.
func NewMessagingPublisher(publisher messaging.Publisher) *MessagingPublisher {
	if publisher == nil {
		panic("outbox: NewMessagingPublisher requires a non-nil messaging.Publisher")
	}
	return &MessagingPublisher{publisher: publisher}
}

// Publish converts the outbox Entry into a messaging.Message and
// delegates to the wrapped messaging.Publisher.
//
// Header conversion: Entry.Headers is JSON-encoded map[string]string
// per the storage contract; HeadersMap parses it. A nil/empty Headers
// field maps to nil — backends already accept that.
func (m *MessagingPublisher) Publish(ctx context.Context, entry Entry) error {
	headers, err := entry.HeadersMap()
	if err != nil {
		return redact.WrapError("outbox: messaging publisher decode headers", err)
	}
	msg := messaging.Message{
		ID:      entry.MessageID,
		Type:    entry.MessageType,
		Payload: entry.Payload,
		Headers: headers,
	}
	if err := m.publisher.Publish(ctx, entry.Topic, entry.RoutingKey, msg); err != nil {
		return redact.WrapError("outbox: messaging publisher delegate", err)
	}
	return nil
}

// Compile-time interface check.
var _ Publisher = (*MessagingPublisher)(nil)
