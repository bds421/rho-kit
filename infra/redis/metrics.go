package redis

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/observability/promutil"
)

// RedisMetrics holds all Prometheus collectors for Redis command and connection monitoring.
type RedisMetrics struct {
	commandDuration        *prometheus.HistogramVec
	commandErrors          *prometheus.CounterVec
	connectionPoolHits     *prometheus.GaugeVec
	connectionPoolMisses   *prometheus.GaugeVec
	connectionPoolTimeouts *prometheus.GaugeVec
	connectionPoolSize     *prometheus.GaugeVec
	connectionPoolIdle     *prometheus.GaugeVec
	connectionPoolStale    *prometheus.GaugeVec
	reconnectAttempts      *prometheus.CounterVec
	reconnectSuccesses     *prometheus.CounterVec
	connectionHealthy      *prometheus.GaugeVec
}

// NewRedisMetrics creates and registers Redis metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewRedisMetrics(reg prometheus.Registerer) *RedisMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &RedisMetrics{
		commandDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "redis",
				Name:      "command_duration_seconds",
				Help:      "Duration of Redis commands in seconds.",
				Buckets:   []float64{0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
			},
			[]string{"instance", "command"},
		),
		commandErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Name:      "command_errors_total",
				Help:      "Total number of Redis command errors.",
			},
			[]string{"instance", "command"},
		),
		connectionPoolHits: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_hits",
				Help:      "Snapshot of connection pool hit count (reused connections).",
			},
			[]string{"instance"},
		),
		connectionPoolMisses: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_misses",
				Help:      "Snapshot of connection pool miss count (new connections).",
			},
			[]string{"instance"},
		),
		connectionPoolTimeouts: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_timeouts",
				Help:      "Snapshot of connection pool timeout count.",
			},
			[]string{"instance"},
		),
		connectionPoolSize: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_total_conns",
				Help:      "Total number of connections in the pool.",
			},
			[]string{"instance"},
		),
		connectionPoolIdle: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_idle_conns",
				Help:      "Number of idle connections in the pool.",
			},
			[]string{"instance"},
		),
		connectionPoolStale: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_stale_conns",
				Help:      "Number of stale connections removed from the pool.",
			},
			[]string{"instance"},
		),
		reconnectAttempts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Name:      "reconnect_attempts_total",
				Help:      "Total number of reconnection attempts.",
			},
			[]string{"instance"},
		),
		reconnectSuccesses: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Name:      "reconnect_successes_total",
				Help:      "Total number of successful reconnections.",
			},
			[]string{"instance"},
		),
		connectionHealthy: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "connection_healthy",
				Help:      "Whether the Redis connection is healthy (1) or not (0).",
			},
			[]string{"instance"},
		),
	}

	mustRegisterAll(reg,
		m.commandDuration, m.commandErrors,
		m.connectionPoolHits, m.connectionPoolMisses, m.connectionPoolTimeouts,
		m.connectionPoolSize, m.connectionPoolIdle, m.connectionPoolStale,
		m.reconnectAttempts, m.reconnectSuccesses, m.connectionHealthy,
	)

	return m
}

// mustRegisterAll registers all collectors, silently reusing existing collectors
// on AlreadyRegisteredError.
func mustRegisterAll(reg prometheus.Registerer, cs ...prometheus.Collector) {
	for _, c := range cs {
		promutil.RegisterCollector(reg, c)
	}
}

// defaultMetrics is the package-level metrics instance for backward compatibility.
var defaultMetrics = NewRedisMetrics(nil)

// knownCommands is an allowlist of Redis command names used as Prometheus
// label values. Commands not in this set are bucketed as "other" to prevent
// unbounded label cardinality (e.g. from client.Do with dynamic commands).
var knownCommands = map[string]struct{}{
	"append": {}, "bitcount": {}, "blmove": {}, "blpop": {}, "brpop": {},
	"decr": {}, "decrby": {}, "del": {}, "dump": {}, "eval": {}, "evalsha": {},
	"exists": {}, "expire": {}, "expireat": {}, "flushdb": {},
	"get": {}, "getdel": {}, "getex": {}, "getrange": {}, "getset": {},
	"hdel": {}, "hexists": {}, "hget": {}, "hgetall": {}, "hincrby": {},
	"hincrbyfloat": {}, "hkeys": {}, "hlen": {}, "hmget": {}, "hmset": {},
	"hset": {}, "hsetnx": {}, "hvals": {},
	"incr": {}, "incrby": {}, "incrbyfloat": {},
	"llen": {}, "lmove": {}, "lpop": {}, "lpos": {}, "lpush": {}, "lpushx": {},
	"lrange": {}, "lrem": {}, "lset": {}, "ltrim": {},
	"mget": {}, "mset": {}, "msetnx": {},
	"persist": {}, "pexpire": {}, "pexpireat": {}, "ping": {}, "psetex": {},
	"pttl": {}, "publish": {},
	"rename": {}, "renamenx": {}, "restore": {}, "rpop": {}, "rpoplpush": {},
	"rpush": {}, "rpushx": {},
	"sadd": {}, "scard": {}, "sdiff": {}, "sdiffstore": {},
	"set": {}, "setex": {}, "setnx": {}, "setrange": {},
	"sinter": {}, "sinterstore": {}, "sismember": {}, "smembers": {},
	"smove": {}, "sort": {}, "spop": {}, "srandmember": {}, "srem": {},
	"strlen": {}, "subscribe": {}, "sunion": {}, "sunionstore": {},
	"ttl": {}, "type": {}, "unsubscribe": {},
	"pipeline": {}, // internal label for pipeline hook
	"xack":     {}, "xadd": {}, "xautoclaim": {}, "xclaim": {}, "xdel": {},
	"xgroup": {}, "xinfo": {}, "xlen": {}, "xpending": {},
	"xrange": {}, "xread": {}, "xreadgroup": {}, "xrevrange": {}, "xtrim": {},
	"zadd": {}, "zcard": {}, "zcount": {}, "zincrby": {}, "zrange": {},
	"zrangebyscore": {}, "zrank": {}, "zrem": {}, "zremrangebyrank": {},
	"zremrangebyscore": {}, "zrevrange": {}, "zrevrangebyscore": {},
	"zrevrank": {}, "zscore": {},
}

// safeCommandName returns the command name if it's in the allowlist,
// otherwise returns "other" to prevent unbounded Prometheus label cardinality.
func safeCommandName(name string) string {
	lower := strings.ToLower(name)
	if _, ok := knownCommands[lower]; ok {
		return lower
	}
	return "other"
}

// metricsHook implements redis.Hook to record command latency and errors.
type metricsHook struct {
	instance string
	metrics  *RedisMetrics
}

var _ redis.Hook = (*metricsHook)(nil)

func (h *metricsHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *metricsHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		start := time.Now()
		err := next(ctx, cmd)
		duration := time.Since(start).Seconds()

		name := safeCommandName(cmd.Name())
		h.metrics.commandDuration.WithLabelValues(h.instance, name).Observe(duration)
		if err != nil && !errors.Is(err, redis.Nil) {
			h.metrics.commandErrors.WithLabelValues(h.instance, name).Inc()
		}
		return err
	}
}

func (h *metricsHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		start := time.Now()
		err := next(ctx, cmds)
		duration := time.Since(start).Seconds()

		// Record the pipeline as a whole — individual command durations are
		// unavailable in pipeline mode.
		h.metrics.commandDuration.WithLabelValues(h.instance, "pipeline").Observe(duration)

		for _, cmd := range cmds {
			if cmd.Err() != nil && !errors.Is(cmd.Err(), redis.Nil) {
				h.metrics.commandErrors.WithLabelValues(h.instance, safeCommandName(cmd.Name())).Inc()
			}
		}
		return err
	}
}

// CollectPoolMetrics snapshots the connection pool stats and updates gauges.
// For automatic collection, use StartPoolMetricsCollector instead.
//
// The instance parameter is used as a Prometheus label to distinguish multiple
// connections (e.g. "cache", "streams"). Use a small, static set of names.
// Panics if instance name is invalid.
//
// Note: only works with *redis.Client (not ClusterClient or Ring). For
// cluster clients or nil, this is a no-op.
func CollectPoolMetrics(client redis.UniversalClient, instance string) {
	collectPoolMetrics(defaultMetrics, client, instance)
}

func collectPoolMetrics(m *RedisMetrics, client redis.UniversalClient, instance string) {
	if err := ValidateName(instance, "instance"); err != nil {
		panic("redis: " + err.Error())
	}
	if client == nil {
		return
	}
	c, ok := client.(*redis.Client)
	if !ok {
		return
	}
	stats := c.PoolStats()
	m.connectionPoolHits.WithLabelValues(instance).Set(float64(stats.Hits))
	m.connectionPoolMisses.WithLabelValues(instance).Set(float64(stats.Misses))
	m.connectionPoolTimeouts.WithLabelValues(instance).Set(float64(stats.Timeouts))
	m.connectionPoolSize.WithLabelValues(instance).Set(float64(stats.TotalConns))
	m.connectionPoolIdle.WithLabelValues(instance).Set(float64(stats.IdleConns))
	m.connectionPoolStale.WithLabelValues(instance).Set(float64(stats.StaleConns))
}

// PoolCollectorOption configures the pool metrics collector.
type PoolCollectorOption func(*poolCollectorConfig)

type poolCollectorConfig struct {
	metrics *RedisMetrics
}

// WithPoolMetrics uses a custom RedisMetrics instance for pool metrics
// collection instead of the default global metrics. Use this when the
// Connection was created with WithRegisterer to ensure pool metrics are
// emitted to the same registerer.
func WithPoolMetrics(m *RedisMetrics) PoolCollectorOption {
	return func(c *poolCollectorConfig) { c.metrics = m }
}

// StartPoolMetricsCollector collects connection pool metrics at the given
// interval. It blocks until ctx is cancelled — call with go:
//
//	go redis.StartPoolMetricsCollector(ctx, client, "cache", 15*time.Second)
//
// The instance parameter is used as a Prometheus label to distinguish multiple
// connections (e.g. "cache", "streams"). Panics if instance name or interval
// is invalid.
func StartPoolMetricsCollector(ctx context.Context, client redis.UniversalClient, instance string, interval time.Duration, opts ...PoolCollectorOption) {
	cfg := poolCollectorConfig{metrics: defaultMetrics}
	for _, o := range opts {
		o(&cfg)
	}

	// Validate early so panics occur at call site, not asynchronously on first tick.
	if err := ValidateName(instance, "instance"); err != nil {
		panic("redis: " + err.Error())
	}
	if interval <= 0 {
		panic("redis: pool metrics collector interval must be positive")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collectPoolMetrics(cfg.metrics, client, instance)
		}
	}
}
