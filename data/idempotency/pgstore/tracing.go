package pgstore

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "kit/data/idempotency/pgstore"

// startSpan starts an OTel span for an idempotency-store operation.
// The key is NOT attached as an attribute — idempotency keys are
// caller-derived and frequently include request fingerprints or
// user-controlled values that should not enter a trace payload.
//
// Nil-safe: a nil receiver still starts a span so the
// uninitialised-receiver error paths continue to return the
// sentinel rather than nil-derefing here.
func (s *Store) startSpan(ctx context.Context, op string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("kit.idempotency.backend", "pgstore"),
		),
	)
}

// recordResult marks the span's error status from the operation's
// error return. A "not found" miss is normal control flow for
// idempotency stores and is left to the call sites to differentiate
// from real backend failures via a boolean second return.
func recordResult(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.SetStatus(codes.Error, "")
	span.RecordError(err)
}
