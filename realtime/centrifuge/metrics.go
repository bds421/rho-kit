package centrifuge

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Metrics holds the kit-side Prometheus collectors emitted by
// [Node]. centrifuge has its own internal metrics (connection
// counts, message rates) which it registers separately when
// configured to do so; this set captures the lifecycle and
// classification-cardinality dimensions the kit cares about.
//
// Label discipline:
//
//   - `outcome` is a fixed enum on connect events
//     (accepted / rejected / error).
//   - `class` is the operator-supplied channel class returned by
//     [ChannelClassifier]; values are projected through
//     [promutil.OpaqueLabelValue] as a cardinality safety net so a
//     misbehaving classifier cannot inflate the label set.
type Metrics struct {
	connectsTotal     *prometheus.CounterVec
	disconnectsTotal  *prometheus.CounterVec
	subscribesTotal   *prometheus.CounterVec
	publishesTotal    *prometheus.CounterVec
}

// MetricsOption configures [NewMetrics].
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer for the kit-side
// metric set. Nil panics so a misconfigured-but-configured caller
// surfaces at startup. Omit for [prometheus.DefaultRegisterer].
//
// Naming: per the root AGENTS.md "Metrics" convention, every inner
// MetricsOption uses WithRegisterer; WithMetricsRegisterer is
// reserved for the outer module-Option wrappers
// (ConnOption/ServerOption/etc.) that thread a registerer through to
// the inner metrics builder. Use WithRegisterer here when passing to
// [NewMetrics]; outer wiring lives at the centrifuge module level.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("realtime/centrifuge: WithRegisterer requires a non-nil registerer")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics constructs and registers the kit-side centrifuge
// metric set on the supplied registerer (or
// [prometheus.DefaultRegisterer]). Repeated calls reuse already-
// registered collectors.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("realtime/centrifuge: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	m := &Metrics{
		connectsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "realtime",
			Subsystem: "centrifuge",
			Name:      "connects_total",
			Help:      "Total centrifuge connection attempts by outcome (accepted=auth passed, rejected=auth refused, error=internal failure).",
		}, []string{"outcome"}),
		disconnectsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "realtime",
			Subsystem: "centrifuge",
			Name:      "disconnects_total",
			Help:      "Total centrifuge disconnects by reason (clean=client-initiated, stale=server kicked).",
		}, []string{"reason"}),
		subscribesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "realtime",
			Subsystem: "centrifuge",
			Name:      "subscribes_total",
			Help:      "Total centrifuge channel subscriptions by channel class.",
		}, []string{"class"}),
		publishesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "realtime",
			Subsystem: "centrifuge",
			Name:      "publishes_total",
			Help:      "Total messages published to centrifuge channels by channel class.",
		}, []string{"class"}),
	}
	m.connectsTotal = promutil.MustRegisterOrGet(reg, m.connectsTotal)
	m.disconnectsTotal = promutil.MustRegisterOrGet(reg, m.disconnectsTotal)
	m.subscribesTotal = promutil.MustRegisterOrGet(reg, m.subscribesTotal)
	m.publishesTotal = promutil.MustRegisterOrGet(reg, m.publishesTotal)
	return m
}

// Connect outcome labels.
const (
	connectOutcomeAccepted = "accepted"
	connectOutcomeRejected = "rejected"
	connectOutcomeError    = "error"
)

const (
	disconnectReasonClean = "clean"
	disconnectReasonStale = "stale"
)

func (m *Metrics) observeConnect(outcome string) {
	if m == nil {
		return
	}
	m.connectsTotal.WithLabelValues(outcome).Inc()
}

func (m *Metrics) observeDisconnect(reason string) {
	if m == nil {
		return
	}
	m.disconnectsTotal.WithLabelValues(reason).Inc()
}

func (m *Metrics) observeSubscribe(class string) {
	if m == nil {
		return
	}
	m.subscribesTotal.WithLabelValues(safeClass(class)).Inc()
}

func (m *Metrics) observePublish(class string) {
	if m == nil {
		return
	}
	m.publishesTotal.WithLabelValues(safeClass(class)).Inc()
}

// safeClass keeps the classifier label human-readable while
// catching the obvious cardinality footguns: an empty value (set
// the bucket to "default") or a value that fails the kit's static-
// label regex (project through [promutil.OpaqueLabelValue] so a
// classifier returning a high-entropy string — channel name with a
// tenant UUID, for instance — cannot inflate the label set even
// though the human-readable shape is the operator's intent).
//
// In practice classifiers should return strings like "user", "room",
// "system" and never hit either branch.
func safeClass(class string) string {
	if class == "" {
		return "default"
	}
	if err := promutil.ValidateStaticLabelValue("class", class); err != nil {
		return promutil.OpaqueLabelValue("class", class)
	}
	return class
}
