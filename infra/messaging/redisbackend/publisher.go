package redisbackend

import (
	"context"

	stream "github.com/bds421/rho-kit/data/stream/redisstream/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// Publisher wraps a stream.Producer to satisfy messaging.MessagePublisher.
// The exchange parameter maps to the Redis stream name; routingKey is stored
// as a message header but is otherwise unused by Redis Streams.
type Publisher struct {
	producer *stream.Producer
}

// NewPublisher creates a Publisher backed by the given StreamProducer.
// Panics if producer is nil — the wrapper dereferences it on every Publish.
func NewPublisher(producer *stream.Producer) *Publisher {
	if producer == nil {
		panic("redisbackend: NewPublisher requires a non-nil *stream.Producer")
	}
	return &Publisher{producer: producer}
}

// Publish writes a message to the Redis stream identified by exchange.
// The routingKey is stored in message headers for consumer inspection.
func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, msg messaging.Message) error {
	sm := toStreamMessage(msg)
	if routingKey != "" {
		sm = sm.WithHeader("routing_key", routingKey)
	}
	_, err := p.producer.Publish(ctx, exchange, sm)
	return err
}
