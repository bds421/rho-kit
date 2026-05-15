package ratelimit

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

const (
	rateLimitKindIP    = "ip"
	rateLimitKindKeyed = "keyed"

	rateLimitOutcomeAllowed             = "allowed"
	rateLimitOutcomeLimited             = "limited"
	rateLimitOutcomeInvalidClientIP     = "invalid_client_ip"
	rateLimitOutcomeInvalidKey          = "invalid_key"
	rateLimitOutcomeUnavailable         = "unavailable"
	rateLimitOutcomeDegradedPassthrough = "degraded_passthrough"
	rateLimitOutcomeDegradedRejected    = "degraded_rejected"

	defaultLimiterName = "default"
)

// Metrics holds Prometheus collectors for rate-limit decisions.
//
// The label set is deliberately small: limiter is caller-provided and should
// be a static name such as "public_api" or "login", kind is "ip" or "keyed",
// and outcome is one of the package-defined outcome constants. Raw keys, IPs,
// tenant IDs, user IDs, and paths are never exported as labels.
type Metrics struct {
	decisions  *prometheus.CounterVec
	retryAfter *prometheus.HistogramVec

	// activeKeys is a custom Collector that walks each registered
	// KeyedLimiter's shards on demand at scrape time. A
	// misconfigured key extractor that explodes the per-shard LRU
	// surfaces as `http_ratelimit_keyed_limiter_active_keys{limiter}`
	// before it pages on memory. Limiters are attached lazily via
	// [WithKeyedMetrics] (or [Metrics.trackKeyedLimiter] directly).
	activeKeys *keyedActiveKeysCollector
}

// MetricsOption configures rate-limit metrics construction.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for the rate-limit
// metrics. When unset, [prometheus.DefaultRegisterer] is used.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("middleware/ratelimit: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers rate-limit metrics. Pass
// [WithRegisterer] to use a non-default registry. Repeated calls reuse
// already-registered collectors on the same registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("middleware/ratelimit: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	m := &Metrics{
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "http",
			Subsystem: "ratelimit",
			Name:      "decisions_total",
			Help:      "Total rate-limit decisions by limiter, limiter kind, and outcome.",
		}, []string{"limiter", "kind", "outcome"}),
		retryAfter: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "http",
			Subsystem: "ratelimit",
			Name:      "retry_after_seconds",
			Help:      "Retry-After seconds emitted for rejected rate-limited requests.",
			Buckets:   []float64{1, 2, 5, 10, 30, 60, 300, 900, 3600},
		}, []string{"limiter", "kind"}),
	}
	m.decisions = promutil.MustRegisterOrGet(cfg.registerer, m.decisions)
	m.retryAfter = promutil.MustRegisterOrGet(cfg.registerer, m.retryAfter)

	m.activeKeys = &keyedActiveKeysCollector{
		desc: prometheus.NewDesc(
			"http_ratelimit_keyed_limiter_active_keys",
			"Approximate number of tracked keys per keyed rate limiter, summed across all internal shards. Collected on-demand at scrape time so a misconfigured key extractor that explodes the LRU surfaces before it pages on memory.",
			[]string{"limiter"},
			nil,
		),
	}
	// Use MustRegisterOrGet to fold idempotent re-registration onto the
	// same collector instance — duplicate NewMetrics calls against the
	// same registerer share the limiter list rather than fighting over
	// two collectors that emit conflicting series.
	m.activeKeys = promutil.MustRegisterOrGet(cfg.registerer, m.activeKeys)
	return m
}

// trackKeyedLimiter wires the limiter into the active-keys collector
// so its per-shard sizes appear in the next scrape. Idempotent.
func (m *Metrics) trackKeyedLimiter(rl *KeyedLimiter) {
	if m == nil || m.activeKeys == nil {
		return
	}
	m.activeKeys.add(rl)
}

// keyedActiveKeysCollector is a [prometheus.Collector] that walks each
// registered [KeyedLimiter]'s shards on demand and emits a single
// `keyed_limiter_active_keys{limiter}` sample per scrape. Implementing
// Collector instead of a GaugeVec avoids polling overhead between
// scrapes — len(shards[i]) is only computed when Prometheus actually
// asks for it.
type keyedActiveKeysCollector struct {
	desc *prometheus.Desc

	mu       sync.RWMutex
	limiters []*KeyedLimiter
}

// Describe implements [prometheus.Collector].
func (c *keyedActiveKeysCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect implements [prometheus.Collector]. Walks each registered
// limiter's shards under each shard's mutex (matching the read path).
// A misconfigured key extractor that explodes the LRU surfaces as a
// rising sample value before it pages on memory.
func (c *keyedActiveKeysCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	limiters := append([]*KeyedLimiter(nil), c.limiters...)
	c.mu.RUnlock()

	for _, rl := range limiters {
		if rl == nil {
			continue
		}
		total := 0
		for i := range rl.shards {
			s := &rl.shards[i]
			s.mu.Lock()
			if s.entries != nil {
				total += s.entries.Len()
			}
			s.mu.Unlock()
		}
		ch <- prometheus.MustNewConstMetric(
			c.desc,
			prometheus.GaugeValue,
			float64(total),
			rl.Name(),
		)
	}
}

func (c *keyedActiveKeysCollector) add(rl *KeyedLimiter) {
	if rl == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, existing := range c.limiters {
		if existing == rl {
			return
		}
	}
	c.limiters = append(c.limiters, rl)
}

func (m *Metrics) observeDecision(limiter, kind, outcome string) {
	if m == nil {
		return
	}
	m.decisions.WithLabelValues(limiter, kind, outcome).Inc()
}

func (m *Metrics) observeRetryAfter(limiter, kind string, seconds float64) {
	if m == nil {
		return
	}
	m.retryAfter.WithLabelValues(limiter, kind).Observe(seconds)
}

func normalizeLimiterName(name string) string {
	if name == "" {
		return defaultLimiterName
	}
	if err := promutil.ValidateStaticLabelValue("limiter name", name); err != nil {
		panic("middleware/ratelimit: invalid limiter name")
	}
	return name
}
