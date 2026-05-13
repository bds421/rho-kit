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

// NewComputeMetrics creates and registers ComputeCache metrics with the given
// registerer. If reg is nil, prometheus.DefaultRegisterer is used.
func NewComputeMetrics(reg prometheus.Registerer) *ComputeMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

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

// WithComputePrometheusMetrics enables Prometheus metrics on the ComputeCache.
// If reg is nil, prometheus.DefaultRegisterer is used.
func WithComputePrometheusMetrics(reg prometheus.Registerer) ComputeOption {
	return func(cfg *computeConfig) {
		cfg.metrics = NewComputeMetrics(reg)
	}
}
