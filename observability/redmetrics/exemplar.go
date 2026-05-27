package redmetrics

import (
	"context"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
)

// ExemplarObserver pairs a histogram observation with the active OTel
// span context so Grafana can deep-link a Prometheus bar to its trace.
//
// Most callers should not construct this directly — pass
// [WithHTTPExemplars] / [WithBatchExemplars] when building the RED set
// and the Middleware will populate exemplars automatically. The type is
// exported so callers wiring their own histograms (custom subsystems)
// can reuse the extraction logic.
type ExemplarObserver struct {
	// exemplarsEnabled is an atomic flag so the option Reset hook in
	// tests can toggle exemplars without racing the hot path.
	exemplarsEnabled atomic.Bool
}

// NewExemplarObserver returns an observer with exemplars enabled.
func NewExemplarObserver() *ExemplarObserver {
	e := &ExemplarObserver{}
	e.exemplarsEnabled.Store(true)
	return e
}

// Observe records v on h. When the supplied context carries an active
// OTel SpanContext AND exemplars are enabled on this observer, an
// exemplar carrying trace_id + span_id is attached.
//
// h must implement [prometheus.ExemplarObserver] (every
// *prometheus.HistogramVec.WithLabelValues() result does). When h does
// not, Observe falls back to the plain Observe path.
func (e *ExemplarObserver) Observe(ctx context.Context, h prometheus.Observer, v float64) {
	if e == nil || !e.exemplarsEnabled.Load() {
		h.Observe(v)
		return
	}
	labels := TraceLabels(ctx)
	if len(labels) == 0 {
		h.Observe(v)
		return
	}
	if eo, ok := h.(prometheus.ExemplarObserver); ok {
		eo.ObserveWithExemplar(v, labels)
		return
	}
	h.Observe(v)
}

// TraceLabels extracts {trace_id, span_id} labels from the active OTel
// span on ctx. Returns nil when no span is active or the span context
// is not valid (no propagated trace).
//
// Exported so callers wiring their own histograms outside this package
// can reuse the extraction without re-implementing it.
func TraceLabels(ctx context.Context) prometheus.Labels {
	span := trace.SpanContextFromContext(ctx)
	if !span.IsValid() {
		return nil
	}
	return prometheus.Labels{
		"trace_id": span.TraceID().String(),
		"span_id":  span.SpanID().String(),
	}
}
