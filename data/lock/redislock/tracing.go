package redislock

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/bds421/rho-kit/data/v2/lock"
)

const tracerName = "kit/data/lock/redislock"

func startSpan(ctx context.Context, op string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("kit.lock.backend", "redislock"),
		),
	)
}

// recordResult flags an OTel span with the operation outcome.
// [lock.ErrLockLost] is NOT an error in the distributed-systems sense
// — it signals that the caller's TTL expired during the critical
// section, which is expected control flow that callers must
// reconcile. We attach the lost flag as an attribute but leave the
// span status unset.
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
