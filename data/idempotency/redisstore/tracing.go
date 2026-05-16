package redisstore

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "kit/data/idempotency/redisstore"

func (s *Store) startSpan(ctx context.Context, op string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("kit.idempotency.backend", "redisstore"),
		),
	)
}

func recordResult(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.SetStatus(codes.Error, "")
	span.RecordError(err)
}
