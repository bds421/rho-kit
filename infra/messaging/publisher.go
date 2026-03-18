package messaging

import "context"

// MessagePublisher is the transport-agnostic interface for publishing messages.
// Backend implementations (amqpbackend.Publisher, redisbackend.Publisher) satisfy
// this interface. The OutboxPublisher also implements it, adding buffered
// at-least-once delivery on top of any underlying MessagePublisher.
type MessagePublisher interface {
	Publish(ctx context.Context, exchange, routingKey string, msg Message) error
}
