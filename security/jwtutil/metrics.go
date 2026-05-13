package jwtutil

import (
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// JWKSMetricsCollector is a Prometheus collector that exposes Provider state
// at scrape time. The collector reads Provider counters directly inside
// Collect(), so /metrics scrapes always see the latest fetch/staleness values
// without a background ticker.
//
// The instance label distinguishes multiple Providers in one process (e.g. a
// service that verifies tokens from two trust roots). Keep instance values
// bounded and static.
type JWKSMetricsCollector struct {
	provider *Provider
	instance string
	clock    func() time.Time

	lastSuccessfulFetch *prometheus.Desc
	fetchFailures       *prometheus.Desc
	staleness           *prometheus.Desc
}

// NewJWKSMetricsCollector constructs a collector for p and registers it on
// reg. If reg is nil, [prometheus.DefaultRegisterer] is used. The collector is
// safe to construct repeatedly against the same registerer: an
// AlreadyRegisteredError with an equivalent collector is treated as success
// and the existing collector is returned.
//
// Returns an error when p is nil or instance is empty so wiring bugs surface
// at startup rather than at first scrape.
func NewJWKSMetricsCollector(p *Provider, reg prometheus.Registerer, instance string) (*JWKSMetricsCollector, error) {
	if p == nil {
		return nil, errors.New("jwtutil: NewJWKSMetricsCollector requires a non-nil Provider")
	}
	if instance == "" {
		return nil, errors.New("jwtutil: NewJWKSMetricsCollector requires a non-empty instance label")
	}
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	labels := prometheus.Labels{"instance": instance}
	c := &JWKSMetricsCollector{
		provider: p,
		instance: instance,
		clock:    time.Now,
		lastSuccessfulFetch: prometheus.NewDesc(
			"jwks_last_successful_fetch_timestamp_seconds",
			"Unix timestamp of the most recent successful JWKS fetch; 0 when no fetch has succeeded.",
			nil, labels,
		),
		fetchFailures: prometheus.NewDesc(
			"jwks_fetch_failures_total",
			"Cumulative count of JWKS fetch failures by reason (http|parse|stale-rejected).",
			[]string{"reason"}, labels,
		),
		staleness: prometheus.NewDesc(
			"jwks_staleness_seconds",
			"Time elapsed since the last successful JWKS fetch in seconds; 0 when no fetch has succeeded.",
			nil, labels,
		),
	}

	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(*JWKSMetricsCollector); ok {
				return existing, nil
			}
			return nil, errors.New("jwtutil: JWKS metrics collector already registered with a different type")
		}
		return nil, err
	}
	return c, nil
}

// Describe implements [prometheus.Collector].
func (c *JWKSMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	if c == nil {
		return
	}
	ch <- c.lastSuccessfulFetch
	ch <- c.fetchFailures
	ch <- c.staleness
}

// Collect implements [prometheus.Collector]. Reads provider counters and
// timestamps at scrape time so every sample reflects the live Provider state.
func (c *JWKSMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	if c == nil || c.provider == nil {
		return
	}
	last := c.provider.LastSuccessfulFetch()
	var lastUnix float64
	var staleSeconds float64
	if !last.IsZero() {
		lastUnix = float64(last.Unix())
		clock := c.clock
		if clock == nil {
			clock = time.Now
		}
		staleSeconds = clock().Sub(last).Seconds()
		if staleSeconds < 0 {
			// Clock skew between collector and provider clock can produce
			// a negative delta; clamp to 0 so dashboards never see a
			// nonsense "negative staleness" sample.
			staleSeconds = 0
		}
	}

	ch <- prometheus.MustNewConstMetric(c.lastSuccessfulFetch, prometheus.GaugeValue, lastUnix)
	ch <- prometheus.MustNewConstMetric(c.staleness, prometheus.GaugeValue, staleSeconds)

	ch <- prometheus.MustNewConstMetric(c.fetchFailures, prometheus.CounterValue,
		float64(c.provider.fetchFailHTTP.Load()), string(jwksFailReasonHTTP))
	ch <- prometheus.MustNewConstMetric(c.fetchFailures, prometheus.CounterValue,
		float64(c.provider.fetchFailParse.Load()), string(jwksFailReasonParse))
	ch <- prometheus.MustNewConstMetric(c.fetchFailures, prometheus.CounterValue,
		float64(c.provider.fetchFailStaleRejected.Load()), string(jwksFailReasonStaleRejected))
}
