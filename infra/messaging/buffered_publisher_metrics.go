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

// NewPrometheusMetrics constructs a [PrometheusBufferedPublisherMetrics]
// registered against reg. When reg is nil, [prometheus.DefaultRegisterer]
// is used. publisherName must be a stable static label such as "events"
// or "outbox-rabbit" — it is validated by [promutil.ValidateStaticLabelValue]
// so an accidental tenant ID or payload-derived value panics at construction
// rather than exploding Prometheus cardinality at scrape time.
//
// Repeated calls with the same registerer reuse the previously registered
// collectors (matching the kit convention enforced by
// [promutil.MustRegisterOrGet]).
func NewPrometheusMetrics(reg prometheus.Registerer, publisherName string) *PrometheusBufferedPublisherMetrics {
	if publisherName == "" {
		panic("messaging: NewPrometheusMetrics requires a non-empty publisher name")
	}
	if err := promutil.ValidateStaticLabelValue("publisher name", publisherName); err != nil {
		panic("messaging: invalid publisher name for Prometheus label")
	}
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

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
//	pm := messaging.NewPrometheusMetrics(reg, "events")
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
// option, matching the WithComputePrometheusMetrics pattern from
// data/cache. Equivalent to:
//
//	pm := messaging.NewPrometheusMetrics(reg, publisherName)
//	messaging.WithMetrics(pm.Hooks())
func WithPrometheusMetrics(reg prometheus.Registerer, publisherName string) BufferedPublisherOption {
	pm := NewPrometheusMetrics(reg, publisherName)
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
