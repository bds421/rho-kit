package nats

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// Resource keys under which the Module publishes its connection and
// default publisher. Retrieve them via [Connection], [Publisher].
const (
	ResourceConnectionKey = "github.com/bds421/rho-kit/app/nats.connection"
	ResourcePublisherKey  = "github.com/bds421/rho-kit/app/nats.publisher"
)

// Option configures the NATS [Module] before Builder.Run executes it.
type Option func(*moduleConfig)

type moduleConfig struct {
	messageSizeLimiter messaging.MessageSizeLimiter
}

// WithMessageSizeLimiter installs a serialized-message-size limiter applied
// to the default publisher.
func WithMessageSizeLimiter(l messaging.MessageSizeLimiter) Option {
	return func(c *moduleConfig) {
		c.messageSizeLimiter = l
	}
}

// Module returns an [app.Module] that opens and supervises a NATS
// JetStream connection plus a default publisher. Pass to
// [app.Builder.With].
//
// Transport safety (FR-073) is enforced by [natsbackend.Connect]:
// non-TLS URLs with password/token credentials are rejected unless
// Config.AllowInsecure is set. This mirrors app/redis and app/amqp
// (those bridges panic at Module construction; NATS fails at Connect
// with the same cleartext-credentials guard). Prefer tls:// / wss://
// or an explicit AllowInsecure for local fixtures.
//
// Panics if cfg.URL is empty or invalid.
func Module(cfg natsbackend.Config, opts ...Option) app.Module {
	if cfg.URL == "" {
		panic("nats: Module requires a non-empty URL")
	}
	if err := natsbackend.ValidateURL(cfg.URL); err != nil {
		panic("nats: Module requires a valid URL")
	}
	cloned := mustCloneConfig(cfg)
	mc := moduleConfig{}
	for _, opt := range opts {
		if opt == nil {
			panic("nats: Module option must not be nil")
		}
		opt(&mc)
	}
	return &natsModule{
		cfg:                cloned,
		messageSizeLimiter: mc.messageSizeLimiter,
	}
}

func mustCloneConfig(cfg natsbackend.Config) natsbackend.Config {
	cloned, err := cfg.Clone()
	if err != nil {
		panic("nats: Module requires a valid TLS config")
	}
	return cloned
}

// natsModule wires a [natsbackend.Connection] and an associated
// [natsbackend.Publisher] into the Builder lifecycle.
type natsModule struct {
	app.BaseModule

	cfg natsbackend.Config

	conn      *natsbackend.Connection
	publisher *natsbackend.Publisher
	logger    *slog.Logger

	messageSizeLimiter messaging.MessageSizeLimiter
}

// ModuleName is the registered Module.Name() value.
const ModuleName = "nats"

func (m *natsModule) Name() string { return ModuleName }

func (m *natsModule) Init(ctx context.Context, mc app.ModuleContext) error {
	m.logger = mc.Logger

	// Prefer the Builder's hot-rotation source when ReloadingTLS was
	// wired. natsbackend.Config.Clone() detects the reloading-config
	// shape (InsecureSkipVerify+VerifyConnection) and bypasses the
	// anti-downgrade guard for that intentional path. Caller-supplied
	// TLS material on m.cfg is preserved when the source is absent.
	if mc.TLSCertSource != nil {
		m.cfg.TLS = netutil.ReloadingClientTLS(mc.TLSCertSource)
	}

	conn, err := natsbackend.Connect(ctx, m.cfg)
	if err != nil {
		return fmt.Errorf("nats module: %w", err)
	}
	m.conn = conn
	metrics := natsbackend.NewMetrics()
	m.publisher = conn.NewPublisher(
		natsbackend.WithMessageSizeLimiter(m.messageSizeLimiter),
		natsbackend.WithPublisherMetrics(metrics),
	)

	mc.Logger.Info("nats connection configured", "config", m.cfg)
	return nil
}

func (m *natsModule) Populate(infra *app.Infrastructure) {
	if m.conn != nil {
		infra.SetResource(ResourceConnectionKey, m.conn)
	}
	if m.publisher != nil {
		infra.SetResource(ResourcePublisherKey, m.publisher)
	}
}

// HealthChecks exposes NATS connectivity on /readyz so a broker outage
// fails readiness like the amqp/redis/postgres bridges.
func (m *natsModule) HealthChecks() []health.DependencyCheck {
	if m == nil || m.conn == nil {
		return nil
	}
	conn := m.conn
	return []health.DependencyCheck{{
		Name:     "nats",
		Critical: true,
		Check: func(_ context.Context) string {
			if !conn.Healthy() {
				return health.StatusUnhealthy
			}
			return health.StatusHealthy
		},
	}}
}

func (m *natsModule) Stop(ctx context.Context) error {
	if m == nil || m.conn == nil {
		return nil
	}
	conn := m.conn
	m.conn = nil
	if err := conn.Stop(ctx); err != nil {
		m.logger.Warn("error closing nats", redact.Error(err))
		return err
	}
	return nil
}

// Connection returns the NATS connection published by [Module] under
// [ResourceConnectionKey], or nil if no nats adapter was registered.
func Connection(infra app.Infrastructure) *natsbackend.Connection {
	v, ok := infra.Resource(ResourceConnectionKey)
	if !ok {
		return nil
	}
	c, _ := v.(*natsbackend.Connection)
	return c
}

// Publisher returns the NATS publisher published by [Module] under
// [ResourcePublisherKey], or nil if no nats adapter was registered.
func Publisher(infra app.Infrastructure) *natsbackend.Publisher {
	v, ok := infra.Resource(ResourcePublisherKey)
	if !ok {
		return nil
	}
	p, _ := v.(*natsbackend.Publisher)
	return p
}
