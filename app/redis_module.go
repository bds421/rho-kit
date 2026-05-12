package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/tlsclone"
	kitredis "github.com/bds421/rho-kit/infra/redis/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// redisModule implements the Module interface for Redis connections.
// It handles connection setup, health checks, pool metrics, and cleanup.
type redisModule struct {
	opts     *goredis.Options
	connOpts []kitredis.ConnOption

	// initialized during Init
	conn   *kitredis.Connection
	logger *slog.Logger
}

// newRedisModule creates a Redis module with the given options.
// Panics if opts is nil (startup-time configuration error).
//
// When allowPlaintext is false, the module refuses to construct a connection
// that targets a non-loopback Redis without TLS or without a password. Use
// [Builder.WithoutRedisTLS] to acknowledge plaintext for local-dev fixtures.
func newRedisModule(opts *goredis.Options, allowPlaintext bool, connOpts ...kitredis.ConnOption) *redisModule {
	if opts == nil {
		panic("app: redis options must not be nil")
	}
	if !allowPlaintext {
		enforceRedisTransportSafety(opts)
	}
	return &redisModule{
		opts:     cloneRedisOptions(opts),
		connOpts: append([]kitredis.ConnOption(nil), connOpts...),
	}
}

// enforceRedisTransportSafety panics when opts targets a non-loopback Redis
// without TLS or without a password. Local-dev fixtures (loopback) bypass
// the check; production deployments must either pin TLS + auth or opt out
// with [Builder.WithoutRedisTLS].
func enforceRedisTransportSafety(opts *goredis.Options) {
	if isLoopbackRedisAddr(opts.Addr) {
		return
	}
	if opts.TLSConfig == nil {
		panic("app: WithRedis requires TLSConfig for non-loopback addresses (use WithoutRedisTLS for local dev)")
	}
	if opts.Password == "" {
		panic("app: WithRedis requires a non-empty Password for non-loopback addresses (use WithoutRedisTLS for local dev)")
	}
}

// isLoopbackRedisAddr reports whether addr is a loopback host. Accepts bare
// "localhost", IPv4 loopback ("127.0.0.0/8"), and IPv6 loopback ("::1").
// Empty addresses are treated as loopback so that go-redis defaults
// (localhost:6379) remain dev-safe.
func isLoopbackRedisAddr(addr string) bool {
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

func cloneRedisOptions(opts *goredis.Options) *goredis.Options {
	optsCopy := *opts
	if optsCopy.TLSConfig != nil {
		tlsConfig, err := tlsclone.ConfigWithFloor(optsCopy.TLSConfig, tls.VersionTLS12)
		if err != nil {
			panic("app: redis TLS MaxVersion must allow TLS 1.2 or newer")
		}
		optsCopy.TLSConfig = tlsConfig
	}
	if optsCopy.MaintNotificationsConfig != nil {
		cfg := *optsCopy.MaintNotificationsConfig
		optsCopy.MaintNotificationsConfig = &cfg
	}
	return &optsCopy
}

func (m *redisModule) Name() string { return "redis" }

func (m *redisModule) Init(_ context.Context, mc ModuleContext) error {
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

	mc.Runner.AddFunc("redis-pool-metrics", func(ctx context.Context) error {
		kitredis.StartPoolMetricsCollector(
			ctx, conn.Client(), "default", 15*time.Second,
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

func (m *redisModule) Populate(infra *Infrastructure) {
	infra.Redis = m.conn
}

func (m *redisModule) Stop(_ context.Context) error {
	if m == nil || m.conn == nil {
		return nil
	}
	conn := m.conn
	m.conn = nil
	if err := conn.Close(); err != nil {
		m.logger.Warn("error closing redis", slog.Any("error", err))
		return err
	}
	return nil
}
