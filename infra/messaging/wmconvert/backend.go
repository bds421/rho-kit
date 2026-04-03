package wmconvert

import (
	"log/slog"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/bds421/rho-kit/infra/messaging"
)

// BackendOption configures a Backend.
type BackendOption func(*Backend)

// WithTopicFunc sets the topic mapping function. Default: [ExchangeTopic].
func WithTopicFunc(fn TopicFunc) BackendOption {
	return func(b *Backend) {
		b.topicFn = fn
	}
}

// WithHealthFunc sets the health check function for the Connector.
// Default: always healthy. Use this to wire up backend-specific health checks.
func WithHealthFunc(fn func() bool) BackendOption {
	return func(b *Backend) {
		b.healthFn = fn
	}
}

// Backend wraps a Watermill publisher and subscriber into rho-kit's
// messaging interfaces. It provides [messaging.MessagePublisher],
// [messaging.MessageConsumer], and [messaging.Connector].
//
// Use Backend when you need provider portability (Kafka, NATS, Google Pub/Sub,
// SQL, etc.) rather than the optimized AMQP or Redis Streams backends.
//
// # Quick Start
//
//	// Kafka example
//	kafkaPub, _ := kafka.NewPublisher(kafkaConfig, watermillLogger)
//	kafkaSub, _ := kafka.NewSubscriber(kafkaConfig, watermillLogger)
//
//	backend := wmconvert.NewBackend(kafkaPub, kafkaSub, logger,
//	    wmconvert.WithTopicFunc(wmconvert.RoutingKeyTopic),
//	)
//
//	// Use with rho-kit messaging interfaces
//	err := backend.Publisher().Publish(ctx, "exchange", "user.created", msg)
type Backend struct {
	wmPub  message.Publisher
	wmSub  message.Subscriber
	logger *slog.Logger

	topicFn  TopicFunc
	healthFn func() bool

	publisher *PublisherAdapter
	consumer  *ConsumerAdapter
	connector *ConnectorAdapter
}

// NewBackend creates a Backend from Watermill publisher and subscriber.
// Either pub or sub may be nil if only one direction is needed.
func NewBackend(pub message.Publisher, sub message.Subscriber, logger *slog.Logger, opts ...BackendOption) *Backend {
	if logger == nil {
		logger = slog.Default()
	}

	b := &Backend{
		wmPub:    pub,
		wmSub:    sub,
		logger:   logger,
		topicFn:  ExchangeTopic,
		healthFn: func() bool { return true },
	}

	for _, opt := range opts {
		opt(b)
	}

	if pub != nil {
		b.publisher = NewPublisherAdapter(pub, b.topicFn)
	}
	if sub != nil {
		b.consumer = NewConsumerAdapter(sub, logger)
	}
	b.connector = NewConnectorAdapter(b.healthFn, b.close)

	return b
}

// Publisher returns the messaging.MessagePublisher backed by Watermill.
// Returns nil if no publisher was provided.
func (b *Backend) Publisher() messaging.MessagePublisher {
	if b.publisher == nil {
		return nil
	}
	return b.publisher
}

// Consumer returns the messaging.MessageConsumer backed by Watermill.
// Returns nil if no subscriber was provided.
func (b *Backend) Consumer() messaging.MessageConsumer {
	if b.consumer == nil {
		return nil
	}
	return b.consumer
}

// Connector returns the messaging.Connector for health checks and lifecycle.
func (b *Backend) Connector() messaging.Connector {
	return b.connector
}

// close shuts down both the Watermill publisher and subscriber.
func (b *Backend) close() error {
	var firstErr error
	if b.wmPub != nil {
		if err := b.wmPub.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if b.wmSub != nil {
		if err := b.wmSub.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
