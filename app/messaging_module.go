package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/bds421/rho-kit/core/config"
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
	secretRotation bool

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

	// Secret rotation: watch RABBITMQ_URL_FILE or RABBITMQ_PASSWORD_FILE.
	if m.secretRotation {
		m.startSecretWatcher(mc)
	}

	mc.Logger.Info("rabbitmq connection configured")
	return nil
}

func (m *messagingModule) startSecretWatcher(mc ModuleContext) {
	// Prefer watching the full URL file. Fall back to password-only.
	urlPath := config.GetSecretPath("RABBITMQ_URL")
	pwPath := config.GetSecretPath("RABBITMQ_PASSWORD")

	switch {
	case urlPath != "":
		w := config.NewWatchable(m.url)
		sw := config.NewSecretWatcher("RABBITMQ_URL", w,
			config.WithWatchLogger(mc.Logger),
		)
		w.OnChange(func(_, newURL string) {
			m.conn.UpdateURL(newURL)
		})
		mc.Runner.AddFunc("rabbitmq-secret-watcher", sw.Start)
		mc.Logger.Info("rabbitmq secret rotation enabled", "source", "RABBITMQ_URL_FILE")

	case pwPath != "":
		currentPW := config.GetSecret("RABBITMQ_PASSWORD", "")
		w := config.NewWatchable(currentPW)
		sw := config.NewSecretWatcher("RABBITMQ_PASSWORD", w,
			config.WithWatchLogger(mc.Logger),
		)
		w.OnChange(func(_, newPW string) {
			newURL := replaceURLPassword(m.url, newPW)
			m.conn.UpdateURL(newURL)
		})
		mc.Runner.AddFunc("rabbitmq-secret-watcher", sw.Start)
		mc.Logger.Info("rabbitmq secret rotation enabled", "source", "RABBITMQ_PASSWORD_FILE")
	}
}

// replaceURLPassword replaces the password in an AMQP URL.
func replaceURLPassword(rawURL, newPassword string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	user := ""
	if u.User != nil {
		user = u.User.Username()
	}
	u.User = url.UserPassword(user, newPassword)
	return u.String()
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
