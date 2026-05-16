package pgadvisory

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/bds421/rho-kit/data/v2/lock"
)

const tracerName = "kit/data/lock/pgadvisory"

func startSpan(ctx context.Context, op string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("kit.lock.backend", "pgadvisory"),
		),
	)
}

// recordResult flags an OTel span with the operation outcome.
// [lock.ErrLockLost] is normal control flow (caller no longer holds
// the lock) and stays as a non-error attribute rather than an error
// status — mirrors redislock's tracing helper.
func recordResult(span trace.Span, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, lock.ErrLockLost) {
		span.SetAttributes(attribute.Bool("kit.lock.lost", true))
		return
	}
	span.SetStatus(codes.Error, "")
	span.RecordError(err)
}
