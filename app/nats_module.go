package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// natsModule wires a [natsbackend.Connection] and an associated
// [natsbackend.Publisher] into the Builder lifecycle.
//
// JetStream stream/consumer declarations are caller-driven (via
// [natsbackend.Connection.EnsureStream] inside RouterFunc) so the
// Builder does not impose stream-shape opinions. The module's job
// is connection management + populate Infrastructure so handlers
// can Publish without knowing about the underlying broker.
type natsModule struct {
	BaseModule

	cfg natsbackend.Config

	conn      *natsbackend.Connection
	publisher *natsbackend.Publisher
	logger    *slog.Logger

	messageSizeLimiter messaging.MessageSizeLimiter
}

func newNatsModule(cfg natsbackend.Config) *natsModule {
	if cfg.URL == "" {
		panic("app: WithNATS requires a non-empty URL")
	}
	if err := natsbackend.ValidateURL(cfg.URL); err != nil {
		panic("app: WithNATS requires a valid URL")
	}
	cfg = mustCloneNATSConfig(cfg)
	return &natsModule{
		BaseModule: NewBaseModule("nats"),
		cfg:        cfg,
	}
}

func mustCloneNATSConfig(cfg natsbackend.Config) natsbackend.Config {
	cloned, err := cfg.Clone()
	if err != nil {
		panic("app: WithNATS requires a valid TLS config")
	}
	return cloned
}

func (m *natsModule) Init(ctx context.Context, mc ModuleContext) error {
	m.logger = mc.Logger

	conn, err := natsbackend.Connect(ctx, m.cfg)
	if err != nil {
		return fmt.Errorf("nats module: %w", err)
	}
	m.conn = conn
	metrics := natsbackend.NewMetrics(nil)
	m.publisher = conn.NewPublisher(
		natsbackend.WithMessageSizeLimiter(m.messageSizeLimiter),
		natsbackend.WithPublisherMetrics(metrics),
	)

	mc.Logger.Info("nats connection configured", "config", m.cfg)
	return nil
}

func (m *natsModule) Populate(infra *Infrastructure) {
	infra.NATS = m.conn
	infra.NATSPublisher = m.publisher
}

func (m *natsModule) Stop(ctx context.Context) error {
	if m == nil || m.conn == nil {
		return nil
	}
	conn := m.conn
	m.conn = nil
	if err := conn.Stop(ctx); err != nil {
		m.logger.Warn("error closing nats", slog.Any("error", err))
		return err
	}
	return nil
}
