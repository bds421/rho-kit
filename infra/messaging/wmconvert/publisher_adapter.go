package wmconvert

import (
	"context"
	"fmt"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/bds421/rho-kit/infra/messaging"
)

// TopicFunc computes the Watermill topic from exchange and routing key.
// Each Watermill backend has its own topic naming conventions:
//   - AMQP: topic = exchange (routing key is in AMQP headers)
//   - Redis Streams: topic = stream name (exchange parameter)
//   - Kafka: topic = routing key or exchange
//
// Use [ExchangeTopic] for backends that map topic to exchange (AMQP, Redis),
// or [RoutingKeyTopic] for backends that map topic to routing key (Kafka).
type TopicFunc func(exchange, routingKey string) string

// ExchangeTopic returns the exchange as the Watermill topic.
// Use for backends where the topic maps to a stream/exchange name.
func ExchangeTopic(exchange, _ string) string { return exchange }

// RoutingKeyTopic returns the routing key as the Watermill topic.
// Use for backends where the topic maps to a message type/routing key.
func RoutingKeyTopic(_, routingKey string) string { return routingKey }

// CombinedTopic returns "exchange.routingKey" as the Watermill topic.
func CombinedTopic(exchange, routingKey string) string {
	if exchange == "" {
		return routingKey
	}
	if routingKey == "" {
		return exchange
	}
	return exchange + "." + routingKey
}

// PublisherAdapter wraps a Watermill Publisher to implement messaging.MessagePublisher.
type PublisherAdapter struct {
	publisher message.Publisher
	topicFn   TopicFunc
}

// NewPublisherAdapter creates a PublisherAdapter that publishes rho-kit messages
// through the given Watermill publisher. The topicFn determines how exchange and
// routing key map to the Watermill topic.
func NewPublisherAdapter(pub message.Publisher, topicFn TopicFunc) *PublisherAdapter {
	if pub == nil {
		panic("wmconvert: publisher must not be nil")
	}
	if topicFn == nil {
		topicFn = ExchangeTopic
	}
	return &PublisherAdapter{
		publisher: pub,
		topicFn:   topicFn,
	}
}

// Publish converts a rho-kit Message to a Watermill message and publishes it.
func (a *PublisherAdapter) Publish(ctx context.Context, exchange, routingKey string, msg messaging.Message) error {
	wmMsg := ToWatermill(msg, exchange, routingKey)
	wmMsg.SetContext(ctx)

	topic := a.topicFn(exchange, routingKey)
	if err := a.publisher.Publish(topic, wmMsg); err != nil {
		return fmt.Errorf("wmconvert publish: %w", err)
	}
	return nil
}

// Close closes the underlying Watermill publisher.
func (a *PublisherAdapter) Close() error {
	return a.publisher.Close()
}
