package redis

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Metrics holds all Prometheus collectors for Redis command and connection monitoring.
type Metrics struct {
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

// MetricsOption configures the redis metric constructor. Standardised
// across the kit so every package exposes
// `NewMetrics(opts ...MetricsOption)`.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for Redis metrics.
// The kit-canonical name on the inner [MetricsOption] type; callers
// building a Connection through [Connect] should pass
// [WithMetricsRegisterer] (the ConnOption variant) instead. Passing
// nil panics.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("redis: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers Redis metrics. Pass
// [WithRegisterer] to use a non-default registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("redis: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	m := &Metrics{
		commandDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "redis",
				Name:      "command_duration_seconds",
				Help:      "Duration of Redis commands in seconds.",
				Buckets:   []float64{0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
			},
			[]string{"redis_instance", "command"},
		),
		commandErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Name:      "command_errors_total",
				Help:      "Total number of Redis command errors.",
			},
			[]string{"redis_instance", "command"},
		),
		connectionPoolHits: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_hits",
				Help:      "Snapshot of connection pool hit count (reused connections).",
			},
			[]string{"redis_instance"},
		),
		connectionPoolMisses: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_misses",
				Help:      "Snapshot of connection pool miss count (new connections).",
			},
			[]string{"redis_instance"},
		),
		connectionPoolTimeouts: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_timeouts",
				Help:      "Snapshot of connection pool timeout count.",
			},
			[]string{"redis_instance"},
		),
		connectionPoolSize: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_total_conns",
				Help:      "Total number of connections in the pool.",
			},
			[]string{"redis_instance"},
		),
		connectionPoolIdle: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_idle_conns",
				Help:      "Number of idle connections in the pool.",
			},
			[]string{"redis_instance"},
		),
		connectionPoolStale: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "pool_stale_conns",
				Help:      "Number of stale connections removed from the pool.",
			},
			[]string{"redis_instance"},
		),
		reconnectAttempts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Name:      "reconnect_attempts_total",
				Help:      "Total number of reconnection attempts.",
			},
			[]string{"redis_instance"},
		),
		reconnectSuccesses: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Name:      "reconnect_successes_total",
				Help:      "Total number of successful reconnections.",
			},
			[]string{"redis_instance"},
		),
		connectionHealthy: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Name:      "connection_healthy",
				Help:      "Whether the Redis connection is healthy (1) or not (0).",
			},
			[]string{"redis_instance"},
		),
	}

	m.commandDuration = promutil.MustRegisterOrGet(reg, m.commandDuration)
	m.commandErrors = promutil.MustRegisterOrGet(reg, m.commandErrors)
	m.connectionPoolHits = promutil.MustRegisterOrGet(reg, m.connectionPoolHits)
	m.connectionPoolMisses = promutil.MustRegisterOrGet(reg, m.connectionPoolMisses)
	m.connectionPoolTimeouts = promutil.MustRegisterOrGet(reg, m.connectionPoolTimeouts)
	m.connectionPoolSize = promutil.MustRegisterOrGet(reg, m.connectionPoolSize)
	m.connectionPoolIdle = promutil.MustRegisterOrGet(reg, m.connectionPoolIdle)
	m.connectionPoolStale = promutil.MustRegisterOrGet(reg, m.connectionPoolStale)
	m.reconnectAttempts = promutil.MustRegisterOrGet(reg, m.reconnectAttempts)
	m.reconnectSuccesses = promutil.MustRegisterOrGet(reg, m.reconnectSuccesses)
	m.connectionHealthy = promutil.MustRegisterOrGet(reg, m.connectionHealthy)

	return m
}

// defaultMetrics() returns the package-level metrics instance, lazily
// initialized on first use. Lazy init keeps importing the package side-
// effect-free: Prometheus collector registration happens only when an
// adopter actually constructs a connection without supplying their own
// registerer, never at package-load time. Wires through
// [sync.OnceValue] so concurrent first-callers converge on a single
// instance.
var defaultMetrics = sync.OnceValue(func() *Metrics { return NewMetrics() })

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
// When onReadOnly is set, READONLY replies also flip the Connection's
// sticky read-only flag so degradation policies can branch via
// [ReadOnlyAware].
type metricsHook struct {
	instance   string
	metrics    *Metrics
	onReadOnly func()
}

var _ redis.Hook = (*metricsHook)(nil)

func (h *metricsHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *metricsHook) noteReadOnly(err error) {
	if err == nil || h.onReadOnly == nil {
		return
	}
	if IsReadOnlyError(err) {
		h.onReadOnly()
	}
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
			h.noteReadOnly(err)
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
				h.noteReadOnly(cmd.Err())
			}
		}
		// Pipeline-level error may also be READONLY without per-cmd errs.
		h.noteReadOnly(err)
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
	collectPoolMetrics(defaultMetrics(), client, instance)
}

func collectPoolMetrics(m *Metrics, client redis.UniversalClient, instance string) {
	if err := ValidateName(instance, "instance"); err != nil {
		panic("redis: StartPoolMetricsCollector invalid instance name")
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
	metrics *Metrics
}

// WithPoolMetrics uses a custom Metrics instance for pool metrics
// collection instead of the default global metrics. Use this when the
// Connection was created with WithMetricsRegisterer to ensure pool
// metrics are emitted to the same registerer.
func WithPoolMetrics(m *Metrics) PoolCollectorOption {
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
	cfg := poolCollectorConfig{metrics: defaultMetrics()}
	for _, o := range opts {
		if o == nil {
			panic("redis: StartPoolMetricsCollector pool metrics collector option must not be nil")
		}
		o(&cfg)
	}

	// Validate early so panics occur at call site, not asynchronously on first tick.
	if err := ValidateName(instance, "instance"); err != nil {
		panic("redis: StartPoolMetricsCollector invalid instance name")
	}
	if interval <= 0 {
		panic("redis: StartPoolMetricsCollector pool metrics collector interval must be positive")
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
