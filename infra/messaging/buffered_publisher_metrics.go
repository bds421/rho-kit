package messaging

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

const (
	bufferedPublisherDropReasonBufferFull = "buffer_full"

	bufferedPublisherStateWriteSuccess = "success"
	bufferedPublisherStateWriteError   = "error"
)

// PrometheusBufferedPublisherMetrics exposes the buffered publisher's
// operational signals as default Prometheus collectors. It satisfies the
// "opt-in via existing constructor" contract: callers attach an instance
// via [WithMetrics] and the publisher will call the hooks on the
// underlying [BufferedPublisherMetrics] type.
//
// Labels are intentionally low-cardinality:
//
//   - publisher is a static, caller-provided name (e.g. "events" or
//     "outbox-rabbit"). It is validated as a Prometheus static label so
//     values containing tenant or message-derived data fail fast at
//     construction.
//   - reason on the drop counter is a closed enum (currently only
//     "buffer_full"). It exists so a future drop reason can be added
//     without an incompatible metric break.
//   - outcome on the state-write counter is "success" or "error". The
//     pair gives operators a single time series to alert on (rising
//     error rate) without inventing a second metric.
type PrometheusBufferedPublisherMetrics struct {
	name string

	dropped      *prometheus.CounterVec
	stateWrites  *prometheus.CounterVec
	pending      *prometheus.GaugeVec
	bufferedByte *prometheus.GaugeVec
}

// MetricsOption configures [NewBufferedPublisherMetrics]. Standardised
// across the kit so every metrics constructor uses
// `NewMetrics(opts ...MetricsOption)`.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for buffered
// publisher metrics. When unset, [prometheus.DefaultRegisterer] is
// used. Passing nil panics so a miswired "metrics enabled, registerer
// not supplied" caller surfaces at startup rather than going to the
// global default.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("messaging: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewBufferedPublisherMetrics constructs a
// [PrometheusBufferedPublisherMetrics] for publisherName. publisherName
// must be a stable static label such as "events" or "outbox-rabbit" —
// it is validated by [promutil.ValidateStaticLabelValue] so an
// accidental tenant ID or payload-derived value panics at construction
// rather than exploding Prometheus cardinality at scrape time.
//
// Pass [WithRegisterer] to use a non-default registry. Repeated calls
// reuse the previously registered collectors on the same registry
// (matching the kit convention enforced by
// [promutil.MustRegisterOrGet]).
func NewBufferedPublisherMetrics(publisherName string, opts ...MetricsOption) *PrometheusBufferedPublisherMetrics {
	if publisherName == "" {
		panic("messaging: NewBufferedPublisherMetrics requires a non-empty publisher name")
	}
	if err := promutil.ValidateStaticLabelValue("publisher name", publisherName); err != nil {
		panic("messaging: NewBufferedPublisherMetrics invalid publisher name for Prometheus label")
	}
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("messaging: NewBufferedPublisherMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	m := &PrometheusBufferedPublisherMetrics{
		name: publisherName,
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "buffered_publisher",
			Name:      "dropped_total",
			Help:      "Total messages dropped by the buffered publisher (back-pressure). Reason is a closed enum; today only buffer_full.",
		}, []string{"publisher", "reason"}),
		stateWrites: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "buffered_publisher",
			Name:      "state_writes_total",
			Help:      "Total state-file write attempts by outcome (success|error). A rising error rate indicates disk-full / EROFS / quota and a duplicate-on-restart risk.",
		}, []string{"publisher", "outcome"}),
		pending: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "buffered_publisher",
			Name:      "pending",
			Help:      "Current number of messages buffered awaiting publish.",
		}, []string{"publisher"}),
		bufferedByte: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "buffered_publisher",
			Name:      "buffered_bytes",
			Help:      "Approximate in-memory bytes pending. Sum of Message.Payload lengths; headers and metadata are excluded. Use alongside `pending` to detect silent overflow before back-pressure trips.",
		}, []string{"publisher"}),
	}

	m.dropped = promutil.MustRegisterOrGet(reg, m.dropped)
	m.stateWrites = promutil.MustRegisterOrGet(reg, m.stateWrites)
	m.pending = promutil.MustRegisterOrGet(reg, m.pending)
	m.bufferedByte = promutil.MustRegisterOrGet(reg, m.bufferedByte)

	return m
}

// Hooks returns a [BufferedPublisherMetrics] that delegates the operational
// callbacks to the registered Prometheus collectors. Use with
// [WithMetrics]:
//
//	pm := messaging.NewBufferedPublisherMetrics("events", messaging.WithRegisterer(reg))
//	pub := messaging.NewBufferedPublisher(inner, conn, logger,
//	    messaging.WithStateFile(path),
//	    messaging.WithMetrics(pm.Hooks()),
//	)
//
// The returned hooks set only OnDrop, OnPendingGauge,
// OnBufferedBytesGauge, OnStateWrite, and OnSaveError. The other
// [BufferedPublisherMetrics] fields remain nil so callers can layer
// additional hooks on top (the publisher skips nil callbacks).
func (m *PrometheusBufferedPublisherMetrics) Hooks() *BufferedPublisherMetrics {
	if m == nil {
		return nil
	}
	return &BufferedPublisherMetrics{
		OnDrop:               m.onDrop,
		OnPendingGauge:       m.onPending,
		OnBufferedBytesGauge: m.onBufferedBytes,
		OnStateWrite:         m.onStateWrite,
		OnSaveError:          m.onStateWriteError,
	}
}

// WithPrometheusMetrics is a convenience wrapper that registers
// [PrometheusBufferedPublisherMetrics] and wires the hooks in a single
// option. Equivalent to:
//
//	pm := messaging.NewBufferedPublisherMetrics(publisherName, messaging.WithRegisterer(reg))
//	messaging.WithMetrics(pm.Hooks())
//
// When reg is nil, the kit-wide [prometheus.DefaultRegisterer] is used.
func WithPrometheusMetrics(publisherName string, reg prometheus.Registerer) BufferedPublisherOption {
	var opts []MetricsOption
	if reg != nil {
		opts = append(opts, WithRegisterer(reg))
	}
	pm := NewBufferedPublisherMetrics(publisherName, opts...)
	return WithMetrics(pm.Hooks())
}

func (m *PrometheusBufferedPublisherMetrics) onDrop() {
	if m == nil {
		return
	}
	m.dropped.WithLabelValues(m.name, bufferedPublisherDropReasonBufferFull).Inc()
}

func (m *PrometheusBufferedPublisherMetrics) onPending(count int) {
	if m == nil {
		return
	}
	m.pending.WithLabelValues(m.name).Set(float64(count))
}

func (m *PrometheusBufferedPublisherMetrics) onBufferedBytes(bytes int) {
	if m == nil {
		return
	}
	m.bufferedByte.WithLabelValues(m.name).Set(float64(bytes))
}

func (m *PrometheusBufferedPublisherMetrics) onStateWrite(success bool) {
	if m == nil {
		return
	}
	outcome := bufferedPublisherStateWriteSuccess
	if !success {
		outcome = bufferedPublisherStateWriteError
	}
	m.stateWrites.WithLabelValues(m.name, outcome).Inc()
}

// onStateWriteError increments the error outcome from the OnSaveError
// hook. The dual wiring is intentional: OnStateWrite covers every write
// attempt while OnSaveError fires only on failure, but in drain the save
// happens inside saveLocked which already calls OnStateWrite — so we
// guard against double counting by leaving this hook as a no-op when
// driven from saveLocked. It exists only to satisfy the spec's
// OnStateWriteError exposure; counting is owned by OnStateWrite.
func (m *PrometheusBufferedPublisherMetrics) onStateWriteError(error) {
	// Intentionally a no-op: state-write success/error are recorded by
	// OnStateWrite to avoid double-counting (saveLocked invokes both
	// OnStateWrite(false) and OnSaveError on the same failure).
}
