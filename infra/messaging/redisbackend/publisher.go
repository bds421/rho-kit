package redisbackend

import (
	"context"

	"github.com/bds421/rho-kit/core/v2/redact"
	stream "github.com/bds421/rho-kit/data/stream/redisstream/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

const headerRoutingKey = "routing_key"

// Publisher wraps a stream.Producer to satisfy messaging.Publisher.
// The exchange parameter maps to the Redis stream name; routingKey is stored
// as a message header but is otherwise unused by Redis Streams.
type Publisher struct {
	producer    *stream.Producer
	sizeLimiter messaging.MessageSizeLimiter
}

// PublisherOption configures a Publisher.
type PublisherOption func(*Publisher)

// WithMessageSizeLimiter replaces the publisher's message-size policy.
func WithMessageSizeLimiter(l messaging.MessageSizeLimiter) PublisherOption {
	return func(p *Publisher) { p.sizeLimiter = l }
}

// WithMaxMessageBytes sets the default serialized message-size limit.
func WithMaxMessageBytes(maxBytes int) PublisherOption {
	return func(p *Publisher) {
		p.sizeLimiter = p.sizeLimiter.WithDefaultMaxBytes(maxBytes)
	}
}

// WithoutMaxMessageBytes disables the default size limit. Route-specific
// limits configured with WithRouteMaxMessageBytes still apply.
func WithoutMaxMessageBytes() PublisherOption {
	return func(p *Publisher) {
		p.sizeLimiter = p.sizeLimiter.WithoutDefaultMaxBytes()
	}
}

// WithRouteMaxMessageBytes overrides the message-size limit for one exact
// exchange+routing-key pair. routingKey may be empty for fanout-style routes.
func WithRouteMaxMessageBytes(exchange, routingKey string, maxBytes int) PublisherOption {
	return func(p *Publisher) {
		p.sizeLimiter = p.sizeLimiter.WithRouteMaxBytes(exchange, routingKey, maxBytes)
	}
}

// NewPublisher creates a Publisher backed by the given StreamProducer.
// Panics if producer is nil — the wrapper dereferences it on every Publish.
func NewPublisher(producer *stream.Producer, opts ...PublisherOption) *Publisher {
	if producer == nil {
		panic("redisbackend: NewPublisher requires a non-nil *stream.Producer")
	}
	p := &Publisher{
		producer:    producer,
		sizeLimiter: messaging.DefaultMessageSizeLimiter(),
	}
	for _, opt := range opts {
		if opt == nil {
			panic("redisbackend: Publisher option must not be nil")
		}
		opt(p)
	}
	return p
}

func (p *Publisher) ready() error {
	if p == nil || p.producer == nil {
		return messaging.ErrInvalidPublisher
	}
	return nil
}

// Publish writes a message to the Redis stream identified by exchange.
// The routingKey is stored in message headers for consumer inspection.
func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, msg messaging.Message) error {
	if err := p.ready(); err != nil {
		return err
	}
	if err := messaging.ValidatePublishContext(ctx); err != nil {
		return err
	}
	if err := messaging.ValidatePublishRoute(exchange, routingKey); err != nil {
		return err
	}
	if err := messaging.ValidateMessage(msg); err != nil {
		return err
	}
	if err := p.sizeLimiter.Check(exchange, routingKey, msg); err != nil {
		return err
	}
	sm := toStreamMessage(msg)
	if routingKey != "" {
		var err error
		sm, err = sm.WithHeader(headerRoutingKey, routingKey)
		if err != nil {
			return redact.WrapError("redisbackend: attach routing-key header", err)
		}
	}
	if _, err := p.producer.Publish(ctx, exchange, sm); err != nil {
		return redact.WrapError("redisbackend: publish", err)
	}
	return nil
}
