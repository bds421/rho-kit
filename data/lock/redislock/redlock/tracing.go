package redlock

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/bds421/rho-kit/data/v2/lock"
)

// tracerName scopes the quorum locker's spans. It is distinct from the
// single-instance redislock tracer so operators can tell the two
// backends apart even when both run in the same process.
const tracerName = "kit/data/lock/redislock/redlock"

// startSpan opens a client span for a quorum-lock operation. The
// kit.lock.backend="redlock" attribute mirrors the redislock tracing
// helper (which tags "redislock") so dashboards can filter by backend
// when callers swap a single-instance locker for a quorum locker.
func startSpan(ctx context.Context, op string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("kit.lock.backend", "redlock"),
		),
	)
}

// recordResult flags an OTel span with the operation outcome.
// [lock.ErrLockLost] is NOT an error in the distributed-systems sense
// — it signals that the caller's TTL expired during the critical
// section, which is expected control flow that callers must
// reconcile. We attach the lost flag as an attribute but leave the
// span status unset. Mirrors the redislock recordResult contract.
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
