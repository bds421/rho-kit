package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/infra/messaging"
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend"
	"github.com/bds421/rho-kit/observability/health"
)

// messagingModule implements the Module interface for RabbitMQ connections.
// It handles connection setup, publisher/consumer creation, health checks,
// and cleanup.
type messagingModule struct {
	url            string
	criticalBroker bool

	// initialized during Init
	conn      *amqpbackend.Connection
	publisher *amqpbackend.Publisher
	consumer  messaging.MessageConsumer
	logger    *slog.Logger
}

// newMessagingModule creates a RabbitMQ module with the given URL.
// Panics if url is empty (startup-time configuration error).
func newMessagingModule(url string) *messagingModule {
	if url == "" {
		panic("app: WithRabbitMQ requires a non-empty URL")
	}
	return &messagingModule{url: url}
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

	conn, dialErr := amqpbackend.Dial(m.url, mc.Logger, mqOpts...)
	if dialErr != nil {
		return fmt.Errorf("rabbitmq module: %w", dialErr)
	}
	m.conn = conn

	pub := amqpbackend.NewPublisher(conn, mc.Logger)
	m.publisher = pub
	m.consumer = amqpbackend.NewConsumer(conn, pub, mc.Logger)

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

func (m *messagingModule) Close(_ context.Context) error {
	if m.publisher != nil {
		m.publisher.Close()
	}
	if m.conn == nil {
		return nil
	}
	if err := m.conn.Close(); err != nil {
		m.logger.Warn("error closing rabbitmq", "error", err)
		return err
	}
	return nil
}
