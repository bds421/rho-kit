package redis

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/tlsclone"
	kitredis "github.com/bds421/rho-kit/infra/redis/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ResourceKey is the Infrastructure.Resource key under which the Module
// publishes its initialized [*kitredis.Connection]. Use [Connection] to
// retrieve the typed handle.
const ResourceKey = "github.com/bds421/rho-kit/app/redis"

// Option configures the Redis [Module] before Builder.Run executes it.
type Option func(*moduleConfig)

type moduleConfig struct {
	connOpts       []kitredis.ConnOption
	allowPlaintext bool
}

// WithoutTLS opts out of the FR-077 transport-safety check that [Module]
// applies. Without this opt-in, [Module.Init] refuses to build a connection
// to a non-loopback Redis without TLSConfig and a non-empty Password, because
// plaintext Redis credentials on the wire are a known foot-gun and silent
// downgrade is unacceptable in v2.
//
// Use this only for local-development fixtures where the Redis instance is
// confirmed to be unreachable from outside the host (Docker host-only
// network, ephemeral sidecar). The check is unconditional otherwise —
// there is no KIT_ENV escape hatch.
func WithoutTLS() Option {
	return func(c *moduleConfig) {
		c.allowPlaintext = true
	}
}

// Module returns an [app.Module] that opens and supervises a Redis
// connection with health checks and pool metrics. Pass to [app.Builder.With].
//
// Transport safety (FR-077): non-loopback addresses MUST set
// [goredis.Options.TLSConfig] and a non-empty Password (or a credentials
// provider). Use [WithoutTLS] to acknowledge plaintext for local-dev
// fixtures (the check is unconditional otherwise — there is no KIT_ENV
// escape hatch).
//
// Connection-level tuning goes through [WithConn] so a single Option-tail
// covers both kit-level safety knobs and the underlying kitredis
// connection options:
//
//	redis.Module(opts)
//	redis.Module(opts, redis.WithoutTLS())
//	redis.Module(opts, redis.WithConn(kitredis.WithInstance("primary")))
//
// Panics if cfg is nil or any option is nil.
func Module(cfg *goredis.Options, opts ...Option) app.Module {
	if cfg == nil {
		panic("redis: Module requires non-nil cfg")
	}
	mc := moduleConfig{}
	for _, opt := range opts {
		if opt == nil {
			panic("redis: Module option must not be nil")
		}
		opt(&mc)
	}
	return moduleWithOptions(cfg, mc.allowPlaintext, mc.connOpts...)
}

// WithConn appends [kitredis.ConnOption] values to the underlying
// [kitredis.Connect] call. Use with [Module].
//
// Multiple calls ACCUMULATE rather than overwrite — calling
//
//	redis.Module(cfg, redis.WithConn(opt1, opt2), redis.WithConn(opt3))
//
// is equivalent to
//
//	redis.Module(cfg, redis.WithConn(opt1, opt2, opt3))
//
// with the connection options applied in declaration order. Mirrors the
// kit's With*-with-...args convention; pass everything in one call for
// readability when ordering matters.
func WithConn(opts ...kitredis.ConnOption) Option {
	for _, opt := range opts {
		if opt == nil {
			panic("redis: WithConn connection option must not be nil")
		}
	}
	captured := append([]kitredis.ConnOption(nil), opts...)
	return func(c *moduleConfig) {
		c.connOpts = append(c.connOpts, captured...)
	}
}

func moduleWithOptions(opts *goredis.Options, allowPlaintext bool, connOpts ...kitredis.ConnOption) *redisModule {
	if !allowPlaintext {
		enforceTransportSafety(opts)
	}
	return &redisModule{
		opts:     cloneOptions(opts),
		connOpts: append([]kitredis.ConnOption(nil), connOpts...),
	}
}

// redisModule implements the Module interface for Redis connections.
// It handles connection setup, health checks, pool metrics, and cleanup.
type redisModule struct {
	opts     *goredis.Options
	connOpts []kitredis.ConnOption

	// initialized during Init
	conn   *kitredis.Connection
	logger *slog.Logger
}

func (m *redisModule) Name() string { return "redis" }

func (m *redisModule) Init(_ context.Context, mc app.ModuleContext) error {
	m.logger = mc.Logger

	connOpts := []kitredis.ConnOption{
		kitredis.WithLogger(mc.Logger),
		kitredis.WithLazyConnect(),
	}
	connOpts = append(connOpts, m.connOpts...)

	conn, err := kitredis.Connect(m.opts, connOpts...)
	if err != nil {
		return fmt.Errorf("redis module: %w", err)
	}
	m.conn = conn

	// Forward the connection's metrics instance so pool gauges land
	// on the same registerer connection / command metrics use. Without
	// this, callers that built the connection with WithRegisterer
	// would see pool metrics silently routed to the default registry
	// (R2-007).
	poolMetrics := conn.Metrics()
	mc.Runner.AddFunc("redis-pool-metrics", func(ctx context.Context) error {
		var opts []kitredis.PoolCollectorOption
		if poolMetrics != nil {
			opts = append(opts, kitredis.WithPoolMetrics(poolMetrics))
		}
		kitredis.StartPoolMetricsCollector(
			ctx, conn.Client(), "default", 15*time.Second, opts...,
		)
		return nil
	})

	mc.Logger.Info("redis connection configured")
	return nil
}

func (m *redisModule) HealthChecks() []health.DependencyCheck {
	if m.conn == nil {
		return nil
	}
	return []health.DependencyCheck{kitredis.HealthCheck(m.conn)}
}

func (m *redisModule) Populate(infra *app.Infrastructure) {
	if m.conn == nil {
		return
	}
	infra.SetResource(ResourceKey, m.conn)
}

func (m *redisModule) Stop(_ context.Context) error {
	if m == nil || m.conn == nil {
		return nil
	}
	conn := m.conn
	m.conn = nil
	if err := conn.Close(); err != nil {
		m.logger.Warn("error closing redis", redact.Error(err))
		return err
	}
	return nil
}

// enforceTransportSafety panics when opts targets a non-loopback Redis
// without TLS or without a password. Local-dev fixtures (loopback) bypass
// the check; production deployments must either pin TLS + auth or opt out
// with [WithoutTLS].
func enforceTransportSafety(opts *goredis.Options) {
	if isLoopbackAddr(opts.Addr) {
		return
	}
	if opts.TLSConfig == nil {
		panic("redis: Module requires TLSConfig for non-loopback addresses (use WithoutTLS for local dev)")
	}
	if !optionsHaveCredentials(opts) {
		panic("redis: Module requires Password or a credentials provider for non-loopback addresses (use WithoutTLS for local dev)")
	}
}

func optionsHaveCredentials(opts *goredis.Options) bool {
	if opts == nil {
		return false
	}
	return opts.Password != "" ||
		opts.CredentialsProvider != nil ||
		opts.CredentialsProviderContext != nil ||
		opts.StreamingCredentialsProvider != nil
}

// IsLoopbackAddr reports whether addr is a loopback host. Accepts bare
// "localhost", IPv4 loopback ("127.0.0.0/8"), and IPv6 loopback ("::1").
// Empty addresses are treated as loopback so that go-redis defaults
// (localhost:6379) remain dev-safe.
func IsLoopbackAddr(addr string) bool {
	return isLoopbackAddr(addr)
}

func isLoopbackAddr(addr string) bool {
	if addr == "" {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func cloneOptions(opts *goredis.Options) *goredis.Options {
	optsCopy := *opts
	if optsCopy.TLSConfig != nil {
		tlsConfig, err := tlsclone.ConfigWithFloor(optsCopy.TLSConfig, tls.VersionTLS12)
		if err != nil {
			if errors.Is(err, tlsclone.ErrInsecureSkipVerifyNotPermitted) {
				panic("redis: TLS InsecureSkipVerify=true is not permitted")
			}
			panic("redis: TLS MaxVersion must allow TLS 1.2 or newer")
		}
		optsCopy.TLSConfig = tlsConfig
	}
	if optsCopy.MaintNotificationsConfig != nil {
		cfg := *optsCopy.MaintNotificationsConfig
		optsCopy.MaintNotificationsConfig = &cfg
	}
	return &optsCopy
}

// Connection returns the Redis connection published by the [Module] under
// [ResourceKey], or nil if no redis adapter was registered with the
// Builder. Use this inside [app.RouterFunc] to access the connection.
func Connection(infra app.Infrastructure) *kitredis.Connection {
	v, ok := infra.Resource(ResourceKey)
	if !ok {
		return nil
	}
	conn, _ := v.(*kitredis.Connection)
	return conn
}
