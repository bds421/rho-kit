package circuitbreaker

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "kit/resilience/circuitbreaker"

// startSpan starts an OTel span around a breaker-wrapped call. The
// breaker's name (or `unnamed` when omitted) attaches as
// `kit.breaker.name` for cross-trace correlation with state-change
// logs and metrics.
func (cb *CircuitBreaker) startSpan(ctx context.Context, op string) (context.Context, trace.Span) {
	name := "unnamed"
	if cb != nil {
		if n := cb.cb.Name(); n != "" {
			name = n
		}
	}
	return otel.Tracer(tracerName).Start(ctx, op,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("kit.breaker.name", name),
		),
	)
}

// recordResult flags the span outcome. The breaker's own
// fast-fail sentinel [ErrCircuitOpen] is NOT recorded as an
// error: a tripped circuit is the breaker doing its job and
// should be observable as a distinct attribute rather than an
// error status that would alarm dashboards filtering on "trace
// errors".
func recordResult(span trace.Span, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, ErrCircuitOpen) {
		span.SetAttributes(attribute.Bool("kit.breaker.tripped", true))
		return
	}
	span.SetStatus(codes.Error, "")
	span.RecordError(err)
}
