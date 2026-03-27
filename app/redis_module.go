package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	goredis "github.com/redis/go-redis/v9"

	kitredis "github.com/bds421/rho-kit/infra/redis"
	"github.com/bds421/rho-kit/observability/health"
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
func newRedisModule(opts *goredis.Options, connOpts ...kitredis.ConnOption) *redisModule {
	if opts == nil {
		panic("app: redis options must not be nil")
	}
	return &redisModule{
		opts:     opts,
		connOpts: connOpts,
	}
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

func (m *redisModule) Close(_ context.Context) error {
	if m.conn == nil {
		return nil
	}
	if err := m.conn.Close(); err != nil {
		m.logger.Warn("error closing redis", "error", err)
		return err
	}
	return nil
}
