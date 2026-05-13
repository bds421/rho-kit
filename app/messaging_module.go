package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// messagingModule implements the Module interface for RabbitMQ connections.
// It handles connection setup, publisher/consumer creation, health checks,
// and cleanup.
type messagingModule struct {
	url            string
	urlProvider    func(context.Context) (string, error)
	criticalBroker bool

	// initialized during Init
	conn      *amqpbackend.Connection
	publisher *amqpbackend.Publisher
	consumer  messaging.Consumer
	logger    *slog.Logger

	messageSizeLimiter messaging.MessageSizeLimiter
}

// newMessagingModule creates a RabbitMQ module with the given URL.
// Panics if url is empty (startup-time configuration error).
func newMessagingModule(url string) *messagingModule {
	if url == "" {
		panic("app: WithRabbitMQ requires a non-empty URL")
	}
	return &messagingModule{url: url}
}

func newMessagingModuleWithURLProvider(provider func(context.Context) (string, error)) *messagingModule {
	if provider == nil {
		panic("app: WithRabbitMQURLProvider requires a non-nil provider")
	}
	return &messagingModule{urlProvider: provider}
}

func (m *messagingModule) Name() string { return "rabbitmq" }

func (m *messagingModule) Init(_ context.Context, mc ModuleContext) error {
	m.logger = mc.Logger

	clientTLS, err := mc.Config.TLS.ClientTLS()
	if err != nil {
		return fmt.Errorf("rabbitmq module: build client TLS: %w", err)
	}

	mqOpts := []amqpbackend.DialOption{amqpbackend.WithLazyConnect()}
	if clientTLS != nil {
		mqOpts = append(mqOpts, amqpbackend.WithTLS(clientTLS))
	}
	if m.urlProvider != nil {
		mqOpts = append(mqOpts, amqpbackend.WithURLProvider(m.urlProvider))
	}

	conn, dialErr := amqpbackend.Dial(m.url, mc.Logger, mqOpts...)
	if dialErr != nil {
		return fmt.Errorf("rabbitmq module: %w", dialErr)
	}
	m.conn = conn

	metrics := amqpbackend.NewMetrics(nil)
	pub := amqpbackend.NewPublisher(conn, mc.Logger,
		amqpbackend.WithMessageSizeLimiter(m.messageSizeLimiter),
		amqpbackend.WithPublisherMetrics(metrics),
	)
	m.publisher = pub
	m.consumer = amqpbackend.NewConsumer(conn, pub, mc.Logger, amqpbackend.WithConsumerMetrics(metrics))

	mc.Logger.Info("rabbitmq connection configured")
	return nil
}

func (m *messagingModule) HealthChecks() []health.DependencyCheck {
	if m.conn == nil {
		return nil
	}
	if m.criticalBroker {
		return []health.DependencyCheck{amqpbackend.CriticalHealthCheck(m.conn)}
	}
	return []health.DependencyCheck{amqpbackend.HealthCheck(m.conn)}
}

func (m *messagingModule) Populate(infra *Infrastructure) {
	infra.Broker = m.conn
	infra.Publisher = m.publisher
	infra.Consumer = m.consumer
}

func (m *messagingModule) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if m.publisher != nil {
		m.publisher.Close()
		m.publisher = nil
	}
	if m.conn == nil {
		return nil
	}
	conn := m.conn
	m.conn = nil
	if err := conn.Stop(ctx); err != nil {
		m.logger.Warn("error closing rabbitmq", slog.Any("error", err))
		return err
	}
	return nil
}
