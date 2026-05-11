package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/core/v2/redact"
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
	m.publisher = conn.NewPublisher(natsbackend.WithMessageSizeLimiter(m.messageSizeLimiter))

	mc.Logger.Info("nats connection configured", "config", m.cfg)
	return nil
}

func (m *natsModule) Populate(infra *Infrastructure) {
	infra.NATS = m.conn
	infra.NATSPublisher = m.publisher
}

func (m *natsModule) Close(_ context.Context) error {
	if m.conn == nil {
		return nil
	}
	if err := m.conn.Close(); err != nil {
		m.logger.Warn("error closing nats", redact.Error(err))
		return err
	}
	return nil
}
