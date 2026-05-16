package rediscache

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	sharedcache "github.com/bds421/rho-kit/data/v2/cache"
)

const tracerName = "kit/data/cache/redis"

// startSpan starts an OTel span for a cache operation. The Cache.name
// is attached as `kit.cache.name`; the key itself is intentionally
// NOT attached — keys are caller-controlled and frequently embed
// tenant IDs, user IDs, or other PII that should not enter a trace
// payload.
//
// Nil-safe: a nil receiver returns a useful span (so the
// uninitialised-receiver test paths continue to return the
// `ErrInvalidCache` sentinel rather than nil-derefing here). The
// span attribute set is degraded to omit the name but the span is
// still created so downstream errors are observable.
//
// Callers MUST defer span.End() (the convention used by every kit
// adapter): preferably via `defer func() { recordResult(span, err); span.End() }()`
// so cache-miss outcomes do not pollute span statuses.
func (rc *Cache) startSpan(ctx context.Context, op string) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String("db.system", "redis"),
	}
	if rc != nil && rc.name != "" {
		attrs = append(attrs, attribute.String("kit.cache.name", rc.name))
	}
	return otel.Tracer(tracerName).Start(ctx, op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
}

// recordResult flags an OTel span with the operation outcome. A
// cache miss is NOT an error condition — every well-running cache
// has misses; the kit treats them as normal control flow and leaves
// the span status unset so dashboards can filter on "errors only"
// without false-positive cache-miss noise.
func recordResult(span trace.Span, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, sharedcache.ErrCacheMiss) {
		// Cache miss is expected control flow; do not flag as error.
		span.SetAttributes(attribute.Bool("kit.cache.miss", true))
		return
	}
	span.SetStatus(codes.Error, "")
	span.RecordError(err)
}
