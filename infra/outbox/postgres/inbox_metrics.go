package postgres

import (
	"github.com/bds421/rho-kit/observability/v2/promutil"
	"github.com/prometheus/client_golang/prometheus"
)

// InboxMetrics exposes committed inbox outcomes. It deliberately has no
// consumer or message-ID labels, since either would let untrusted delivery
// data create unbounded Prometheus cardinality.
type InboxMetrics struct {
	processedTotal  prometheus.Counter
	duplicatesTotal prometheus.Counter
	failuresTotal   prometheus.Counter
}

// InboxMetricsOption configures [NewInboxMetrics].
type InboxMetricsOption func(*inboxMetricsConfig)

type inboxMetricsConfig struct{ registerer prometheus.Registerer }

// WithInboxRegisterer uses a custom registerer, normally for test isolation.
func WithInboxRegisterer(reg prometheus.Registerer) InboxMetricsOption {
	return func(c *inboxMetricsConfig) { c.registerer = reg }
}

// NewInboxMetrics creates metrics for durable inbound processing.
func NewInboxMetrics(opts ...InboxMetricsOption) *InboxMetrics {
	cfg := inboxMetricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("outbox/postgres: InboxMetrics option must not be nil")
		}
		opt(&cfg)
	}
	if cfg.registerer == nil {
		panic("outbox/postgres: InboxMetrics registerer must not be nil")
	}
	m := &InboxMetrics{
		processedTotal:  prometheus.NewCounter(prometheus.CounterOpts{Namespace: "inbox", Name: "processed_total", Help: "Committed first-time inbox deliveries."}),
		duplicatesTotal: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "inbox", Name: "duplicates_total", Help: "Inbox deliveries skipped because their receipt already exists."}),
		failuresTotal:   prometheus.NewCounter(prometheus.CounterOpts{Namespace: "inbox", Name: "failures_total", Help: "Inbox deliveries whose local transaction did not commit."}),
	}
	m.processedTotal = promutil.MustRegisterOrGet(cfg.registerer, m.processedTotal)
	m.duplicatesTotal = promutil.MustRegisterOrGet(cfg.registerer, m.duplicatesTotal)
	m.failuresTotal = promutil.MustRegisterOrGet(cfg.registerer, m.failuresTotal)
	return m
}

func (m *InboxMetrics) processed() {
	if m != nil {
		m.processedTotal.Inc()
	}
}
func (m *InboxMetrics) duplicate() {
	if m != nil {
		m.duplicatesTotal.Inc()
	}
}
func (m *InboxMetrics) failure() {
	if m != nil {
		m.failuresTotal.Inc()
	}
}
