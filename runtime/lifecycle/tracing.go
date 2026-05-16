package lifecycle

import (
	"context"
	"errors"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "kit/runtime/lifecycle"

// startComponentSpan starts an OTel span around a Component
// lifecycle operation (Start or Stop). The span carries the
// operator-supplied component name as `kit.component.name`; the
// name is operator-controlled and bounded by the registration call,
// so it is safe to attach as a plain attribute without opaque
// hashing.
func startComponentSpan(ctx context.Context, op, name string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, op,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("kit.component.name", name),
		),
	)
}

// recordComponentResult flags the span with the outcome.
// [http.ErrServerClosed] is the canonical "clean shutdown"
// signal from net/http; it is NOT an error condition and is
// suppressed from the span status, matching the runner's
// errors.Is handling at the goroutine level.
func recordComponentResult(span trace.Span, err error) {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}
	span.SetStatus(codes.Error, "")
	span.RecordError(err)
}
