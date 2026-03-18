package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PoolMetrics holds Prometheus collectors for database pool monitoring.
type PoolMetrics struct {
	OpenConnections prometheus.Gauge
	IdleConnections prometheus.Gauge
	WaitCount       prometheus.Counter
}

// NewPoolMetrics creates and registers the standard set of Prometheus collectors
// for database connection pool monitoring. The namespace parameter is typically
// the service name (e.g. "backend", "file_copier"). The reg parameter sets the
// Prometheus registerer; if nil, prometheus.DefaultRegisterer is used.
//
// Uses tryRegister internally so that duplicate registrations (e.g. in tests
// or multi-instance scenarios) reuse existing collectors instead of panicking.
func NewPoolMetrics(namespace string, reg prometheus.Registerer) PoolMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
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
	panic(err)
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
func ExportPoolMetrics(ctx context.Context, db *sql.DB, m PoolMetrics, interval time.Duration) {
	var lastWaitCount int64
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats := db.Stats()
			m.OpenConnections.Set(float64(stats.OpenConnections))
			m.IdleConnections.Set(float64(stats.Idle))
			delta := stats.WaitCount - lastWaitCount
			if delta > 0 {
				m.WaitCount.Add(float64(delta))
			}
			// Always update lastWaitCount to handle stats reset (e.g., DB replaced).
			// If WaitCount decreased, we reset our baseline rather than adding a
			// negative value to the Prometheus counter.
			lastWaitCount = stats.WaitCount
		}
	}
}
