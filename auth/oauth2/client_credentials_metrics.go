package oauth2

import (
	"github.com/bds421/rho-kit/observability/v2/promutil"
	"github.com/prometheus/client_golang/prometheus"
)

// ClientCredentialsMetrics exposes refresh/cache behaviour without labels
// derived from client IDs, issuers, or tokens.
type ClientCredentialsMetrics struct {
	cacheHitsTotal prometheus.Counter
	refreshesTotal prometheus.Counter
	failuresTotal  prometheus.Counter
	refreshLatency prometheus.Histogram
}

// ClientCredentialsMetricsOption configures [NewClientCredentialsMetrics].
type ClientCredentialsMetricsOption func(*clientCredentialsMetricsConfig)

type clientCredentialsMetricsConfig struct{ registerer prometheus.Registerer }

// WithClientCredentialsRegisterer isolates metrics in tests or routes them to
// a service-specific Prometheus registerer. The default is DefaultRegisterer.
func WithClientCredentialsRegisterer(reg prometheus.Registerer) ClientCredentialsMetricsOption {
	return func(c *clientCredentialsMetricsConfig) { c.registerer = reg }
}

// NewClientCredentialsMetrics creates the collectors used by a
// [ClientCredentials] source.
func NewClientCredentialsMetrics(opts ...ClientCredentialsMetricsOption) *ClientCredentialsMetrics {
	cfg := clientCredentialsMetricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("oauth2: ClientCredentialsMetrics option must not be nil")
		}
		opt(&cfg)
	}
	if cfg.registerer == nil {
		panic("oauth2: ClientCredentialsMetrics registerer must not be nil")
	}
	m := &ClientCredentialsMetrics{
		cacheHitsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "oauth2", Name: "client_credentials_cache_hits_total",
			Help: "Client-credentials token calls served from a valid local cache.",
		}),
		refreshesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "oauth2", Name: "client_credentials_refreshes_total",
			Help: "Successful client-credentials token endpoint refreshes.",
		}),
		failuresTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "oauth2", Name: "client_credentials_refresh_failures_total",
			Help: "Failed client-credentials token endpoint refreshes.",
		}),
		refreshLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "oauth2", Name: "client_credentials_refresh_seconds",
			Help: "Duration of client-credentials token endpoint refreshes.", Buckets: prometheus.DefBuckets,
		}),
	}
	m.cacheHitsTotal = promutil.MustRegisterOrGet(cfg.registerer, m.cacheHitsTotal)
	m.refreshesTotal = promutil.MustRegisterOrGet(cfg.registerer, m.refreshesTotal)
	m.failuresTotal = promutil.MustRegisterOrGet(cfg.registerer, m.failuresTotal)
	m.refreshLatency = promutil.MustRegisterOrGet(cfg.registerer, m.refreshLatency)
	return m
}

func (m *ClientCredentialsMetrics) cacheHit() {
	if m != nil {
		m.cacheHitsTotal.Inc()
	}
}

func (m *ClientCredentialsMetrics) refreshed(seconds float64, success bool) {
	if m != nil {
		m.refreshLatency.Observe(seconds)
		if success {
			m.refreshesTotal.Inc()
		} else {
			m.failuresTotal.Inc()
		}
	}
}
