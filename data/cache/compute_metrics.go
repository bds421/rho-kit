package cache

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// ComputeMetrics holds Prometheus collectors for ComputeCache monitoring.
type ComputeMetrics struct {
	hits        *prometheus.CounterVec
	misses      *prometheus.CounterVec
	staleServes *prometheus.CounterVec
	errors      *prometheus.CounterVec
	// singleflightInflight tracks the live count of singleflight leader
	// goroutines actively computing a value. Climbing inflight under load
	// indicates ComputeFunc latency is dominating the cache layer and that
	// concurrent callers are queuing on the leader rather than hitting the
	// backend.
	singleflightInflight *prometheus.GaugeVec
	// singleflightWait measures how long a follower (a caller that did not
	// win the singleflight leadership for a key) waited from joining the
	// in-flight group until the leader's result arrived (or the follower's
	// context cancelled). High follower wait with low leader inflight is the
	// signature of a slow individual compute path; high wait + high inflight
	// is a thundering herd.
	singleflightWait *prometheus.HistogramVec
	// singleflightFollowers counts callers that joined an in-flight leader
	// rather than becoming the leader themselves — i.e. the singleflight
	// deduplication saved a redundant compute.
	singleflightFollowers *prometheus.CounterVec
}

// MetricsOption configures [NewComputeMetrics]. Standardised across
// the kit so every package exposes `NewMetrics(opts ...MetricsOption)`
// with a uniform [WithRegisterer] entry point.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for compute-cache
// metrics. When unset, [prometheus.DefaultRegisterer] is used. Passing
// nil panics so a miswired "metrics enabled, registerer not supplied"
// caller surfaces at startup rather than going to the global default.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("cache: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewComputeMetrics creates and registers ComputeCache metrics. Pass
// [WithRegisterer] to use a non-default registry. Repeated calls reuse
// already-registered collectors on the same registry.
func NewComputeMetrics(opts ...MetricsOption) *ComputeMetrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("cache: NewComputeMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	m := &ComputeMetrics{
		hits: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "cache",
				Subsystem: "compute",
				Name:      "hits_total",
				Help:      "Total cache compute hits (fresh data returned).",
			},
			[]string{"name"},
		),
		misses: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "cache",
				Subsystem: "compute",
				Name:      "misses_total",
				Help:      "Total cache compute misses (computation triggered).",
			},
			[]string{"name"},
		),
		staleServes: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "cache",
				Subsystem: "compute",
				Name:      "stale_serves_total",
				Help:      "Total stale-while-revalidate serves.",
			},
			[]string{"name"},
		),
		errors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "cache",
				Subsystem: "compute",
				Name:      "errors_total",
				Help:      "Total errors during cache computation.",
			},
			[]string{"name"},
		),
		singleflightInflight: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "cache",
				Subsystem: "compute",
				Name:      "singleflight_inflight",
				Help:      "Current number of in-flight singleflight leaders (foreground computes).",
			},
			[]string{"name"},
		),
		singleflightWait: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "cache",
				Subsystem: "compute",
				Name:      "singleflight_wait_seconds",
				Help:      "Time followers wait for an in-flight singleflight leader to return a result.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"name"},
		),
		singleflightFollowers: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "cache",
				Subsystem: "compute",
				Name:      "singleflight_followers_total",
				Help:      "Total callers that became followers of an in-flight singleflight leader (compute deduplicated).",
			},
			[]string{"name"},
		),
	}

	m.hits = promutil.MustRegisterOrGet(reg, m.hits)
	m.misses = promutil.MustRegisterOrGet(reg, m.misses)
	m.staleServes = promutil.MustRegisterOrGet(reg, m.staleServes)
	m.errors = promutil.MustRegisterOrGet(reg, m.errors)
	m.singleflightInflight = promutil.MustRegisterOrGet(reg, m.singleflightInflight)
	m.singleflightWait = promutil.MustRegisterOrGet(reg, m.singleflightWait)
	m.singleflightFollowers = promutil.MustRegisterOrGet(reg, m.singleflightFollowers)

	return m
}

// WithComputePrometheusMetrics enables Prometheus metrics on the
// ComputeCache. When reg is nil, [prometheus.DefaultRegisterer] is
// used; pass an explicit registerer to scope metrics to a test or
// per-tenant registry.
func WithComputePrometheusMetrics(reg prometheus.Registerer) ComputeOption {
	return func(cfg *computeConfig) {
		var opts []MetricsOption
		if reg != nil {
			opts = append(opts, WithRegisterer(reg))
		}
		cfg.metrics = NewComputeMetrics(opts...)
	}
}
