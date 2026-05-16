package retry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "kit/resilience/retry"

// startSpan starts a span around a retry-wrapped operation. The
// resulting span carries `kit.retry.max_retries` so an operator
// reading a trace knows the policy that produced the attempt
// pattern. Per-attempt details are exposed via [recordAttempt]
// events so a single retry span captures the full attempt
// sequence without spawning a span per attempt (which would
// inflate exporter load on tight retry loops).
func startSpan(ctx context.Context, op string, maxRetries int) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, op,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.Int("kit.retry.max_retries", maxRetries),
		),
	)
}

// recordResult marks the span outcome from the final return.
func recordResult(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.SetStatus(codes.Error, "")
	span.RecordError(err)
}
