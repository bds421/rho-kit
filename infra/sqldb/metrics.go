package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/bds421/rho-kit/observability/v2/promutil"
	"github.com/prometheus/client_golang/prometheus"
)

// PoolMetrics holds Prometheus collectors for database pool monitoring.
type PoolMetrics struct {
	OpenConnections prometheus.Gauge
	IdleConnections prometheus.Gauge
	WaitCount       prometheus.Counter
}

// MetricsOption configures [NewPoolMetrics]. Standardised across the
// kit so every package exposes `NewMetrics(opts ...MetricsOption)`.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for sqldb pool
// metrics. When unset, [prometheus.DefaultRegisterer] is used.
// Passing nil panics so a miswired "metrics enabled, registerer not
// supplied" caller surfaces at startup rather than going to the
// global default.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("sqldb: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewPoolMetrics creates and registers the standard set of Prometheus
// collectors for database connection pool monitoring. The namespace
// parameter is typically the service name (e.g. "backend",
// "file_copier"). Pass [WithRegisterer] to use a non-default registry.
//
// Uses tryRegister internally so that duplicate registrations (e.g. in
// tests or multi-instance scenarios) reuse existing collectors instead
// of panicking.
func NewPoolMetrics(namespace string, opts ...MetricsOption) PoolMetrics {
	if err := promutil.ValidateMetricNamePart("database metric namespace", namespace); err != nil {
		panic("sqldb: NewPoolMetrics: metric namespace is invalid")
	}
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("sqldb: NewPoolMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer
	return PoolMetrics{
		OpenConnections: tryRegister(reg, prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "db",
			Name:      "open_connections",
			Help:      "Number of open database connections.",
		})).(prometheus.Gauge),
		IdleConnections: tryRegister(reg, prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "db",
			Name:      "idle_connections",
			Help:      "Number of idle database connections.",
		})).(prometheus.Gauge),
		WaitCount: tryRegister(reg, prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "db",
			Name:      "wait_count_total",
			Help:      "Total number of connections waited for.",
		})).(prometheus.Counter),
	}
}

// tryRegister attempts to register a Prometheus collector. If it is already
// registered, the existing collector is returned. Panics on unexpected
// registration errors (e.g. metric name conflicts), consistent with
// redis.RegisterCollector.
func tryRegister(reg prometheus.Registerer, c prometheus.Collector) prometheus.Collector {
	err := reg.Register(c)
	if err == nil {
		return c
	}
	var are prometheus.AlreadyRegisteredError
	if errors.As(err, &are) {
		return are.ExistingCollector
	}
	panic("sqldb: metric registration failed")
}

// Note: tryRegister differs from promutil.RegisterCollector because it returns
// the existing collector on AlreadyRegisteredError (needed for type assertions
// in NewPoolMetrics). promutil.RegisterCollector is fire-and-forget.

// ExportPoolMetrics periodically exports database pool stats to Prometheus.
// Blocks until ctx is cancelled.
//
// WARNING: Only one ExportPoolMetrics goroutine should run per *sql.DB. Running
// multiple instances with different PoolMetrics for the same DB will double-count
// the WaitCount counter.
//
// Panics on a nil db (a nil pool would crash on the first Stats call
// every tick) or a non-positive interval (NewTicker panics on
// non-positive durations, and a zero interval would burn CPU). Use a
// nil-collector PoolMetrics field to opt a single metric out: every
// observe call checks the receiver for nil before recording. PoolMetrics
// values whose required Open/Idle/WaitCount collectors are all nil are
// rejected at startup — recording with no live collectors is a wiring
// bug we surface loudly (L096).
func ExportPoolMetrics(ctx context.Context, db *sql.DB, m PoolMetrics, interval time.Duration) {
	if db == nil {
		panic("sqldb: ExportPoolMetrics requires a non-nil *sql.DB")
	}
	if interval <= 0 {
		panic("sqldb: ExportPoolMetrics requires a positive interval")
	}
	if m.OpenConnections == nil && m.IdleConnections == nil && m.WaitCount == nil {
		panic("sqldb: ExportPoolMetrics: PoolMetrics has no live collectors (use NewPoolMetrics)")
	}
	var lastWaitCount int64
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats := db.Stats()
			if m.OpenConnections != nil {
				m.OpenConnections.Set(float64(stats.OpenConnections))
			}
			if m.IdleConnections != nil {
				m.IdleConnections.Set(float64(stats.Idle))
			}
			delta := stats.WaitCount - lastWaitCount
			if delta > 0 && m.WaitCount != nil {
				m.WaitCount.Add(float64(delta))
			}
			// Always update lastWaitCount to handle stats reset (e.g., DB replaced).
			// If WaitCount decreased, we reset our baseline rather than adding a
			// negative value to the Prometheus counter.
			lastWaitCount = stats.WaitCount
		}
	}
}
