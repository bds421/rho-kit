package pgx

import (
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// PoolStatsCollector is a Prometheus collector that exposes pgxpool runtime
// stats. Unlike a periodic ticker that scrapes pool.Stat() on a timer, the
// collector calls pool.Stat() inside Collect() so each /metrics scrape yields
// a fresh snapshot — no skew between scrape interval and refresh interval, no
// background goroutine to manage, and no cost when /metrics is not scraped.
//
// Use [NewPoolStatsCollector] to construct and register one collector per pool.
// The instance label distinguishes multiple pools (e.g. "primary" vs "replica");
// keep the value bounded and static so Prometheus label cardinality is
// predictable.
type PoolStatsCollector struct {
	pool     *pgxpool.Pool
	instance string

	acquiredConns           *prometheus.Desc
	totalConns              *prometheus.Desc
	idleConns               *prometheus.Desc
	maxConns                *prometheus.Desc
	acquireWaitSeconds      *prometheus.Desc
	acquireCount            *prometheus.Desc
	canceledAcquireCount    *prometheus.Desc
}

// NewPoolStatsCollector constructs a collector for p and registers it with reg.
// If reg is nil, [prometheus.DefaultRegisterer] is used. If reg already has an
// equivalent collector registered, the existing one is reused (so repeated
// construction in tests against [prometheus.DefaultRegisterer] does not panic).
//
// Returns an error when p is nil or instance is empty; constructing a
// collector for a closed pool is the caller's responsibility (Collect emits
// zero-valued samples once Stat() returns zero counters).
func NewPoolStatsCollector(p *pgxpool.Pool, reg prometheus.Registerer, instance string) (*PoolStatsCollector, error) {
	if p == nil {
		return nil, errors.New("pgx: NewPoolStatsCollector requires a non-nil pool")
	}
	if instance == "" {
		return nil, errors.New("pgx: NewPoolStatsCollector requires a non-empty instance label")
	}
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	labels := prometheus.Labels{"instance": instance}
	c := &PoolStatsCollector{
		pool:     p,
		instance: instance,
		acquiredConns: prometheus.NewDesc(
			"pgx_pool_acquired_conns",
			"Number of connections currently checked out of the pgx pool.",
			nil, labels,
		),
		totalConns: prometheus.NewDesc(
			"pgx_pool_total_conns",
			"Total number of connections currently owned by the pgx pool (idle + in-use + constructing).",
			nil, labels,
		),
		idleConns: prometheus.NewDesc(
			"pgx_pool_idle_conns",
			"Number of idle connections in the pgx pool.",
			nil, labels,
		),
		maxConns: prometheus.NewDesc(
			"pgx_pool_max_conns",
			"Maximum number of connections the pgx pool will open.",
			nil, labels,
		),
		acquireWaitSeconds: prometheus.NewDesc(
			"pgx_pool_acquire_wait_seconds_total",
			"Cumulative time spent waiting on Acquire() in seconds (sourced from pgxpool AcquireDuration).",
			nil, labels,
		),
		acquireCount: prometheus.NewDesc(
			"pgx_pool_acquire_count_total",
			"Cumulative count of successful Acquire() calls.",
			nil, labels,
		),
		canceledAcquireCount: prometheus.NewDesc(
			"pgx_pool_canceled_acquire_count_total",
			"Cumulative count of Acquire() calls cancelled by ctx before a connection became available.",
			nil, labels,
		),
	}

	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(*PoolStatsCollector); ok {
				return existing, nil
			}
			return nil, errors.New("pgx: pool stats collector already registered with a different type")
		}
		return nil, err
	}
	return c, nil
}

// Describe implements [prometheus.Collector].
func (c *PoolStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	if c == nil {
		return
	}
	ch <- c.acquiredConns
	ch <- c.totalConns
	ch <- c.idleConns
	ch <- c.maxConns
	ch <- c.acquireWaitSeconds
	ch <- c.acquireCount
	ch <- c.canceledAcquireCount
}

// Collect implements [prometheus.Collector] by snapshotting [pgxpool.Pool.Stat]
// at scrape time and emitting one sample per descriptor. The snapshot is
// internally consistent (Stat() returns a single struct), so gauges and
// counters never disagree about the pool's state during a single scrape.
func (c *PoolStatsCollector) Collect(ch chan<- prometheus.Metric) {
	if c == nil || c.pool == nil {
		return
	}
	stats := c.pool.Stat()
	if stats == nil {
		return
	}

	ch <- prometheus.MustNewConstMetric(c.acquiredConns, prometheus.GaugeValue, float64(stats.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.totalConns, prometheus.GaugeValue, float64(stats.TotalConns()))
	ch <- prometheus.MustNewConstMetric(c.idleConns, prometheus.GaugeValue, float64(stats.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.maxConns, prometheus.GaugeValue, float64(stats.MaxConns()))
	ch <- prometheus.MustNewConstMetric(c.acquireWaitSeconds, prometheus.CounterValue, stats.AcquireDuration().Seconds())
	ch <- prometheus.MustNewConstMetric(c.acquireCount, prometheus.CounterValue, float64(stats.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.canceledAcquireCount, prometheus.CounterValue, float64(stats.CanceledAcquireCount()))
}
