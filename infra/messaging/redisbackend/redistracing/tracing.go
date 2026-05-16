// Package redistracing provides OpenTelemetry trace context
// propagation for the kit's Redis Streams messaging backend. Mirrors
// [amqptracing] / [kafkatracing] / [natstracing]: a Carrier over the
// kit's `map[string]string` message headers plus
// StartConsumerSpan / StartPublisherSpan helpers that set Redis-
// specific OTel semantic-conventions attributes.
package redistracing

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "kit/redis-stream"

// Carrier adapts `map[string]string` (the kit's
// `messaging.Message.Headers`) to the [TextMapCarrier] interface
// for W3C trace context propagation through Redis Stream entry
// fields.
type Carrier map[string]string

func (c Carrier) Get(key string) string { return c[key] }
func (c Carrier) Set(key, value string) { c[key] = value }

func (c Carrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// InjectHeaders injects trace context from ctx into the Redis
// Stream entry headers.
func InjectHeaders(ctx context.Context, headers map[string]string) {
	if headers == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, Carrier(headers))
}

// ExtractContext extracts trace context from Redis Stream entry
// headers and returns a derived context with the parent span.
func ExtractContext(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, Carrier(headers))
}

// StartConsumerSpan starts a [trace.SpanKindConsumer] span for
// processing a Redis Stream entry.
func StartConsumerSpan(ctx context.Context, headers map[string]string, operation, stream, group string) (context.Context, trace.Span) {
	ctx = ExtractContext(ctx, headers)
	tracer := otel.Tracer(tracerName)
	return tracer.Start(ctx, operation+" "+stream,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "redis-stream"),
			attribute.String("messaging.operation.type", "process"),
			attribute.String("messaging.destination.name", stream),
			attribute.String("messaging.redis.consumer.group", group),
		),
	)
}

// StartPublisherSpan starts a [trace.SpanKindProducer] span for
// publishing to a Redis Stream. Injects the W3C trace context into
// the supplied headers before returning.
func StartPublisherSpan(ctx context.Context, headers map[string]string, operation, stream string) (context.Context, trace.Span) {
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, operation+" "+stream,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "redis-stream"),
			attribute.String("messaging.operation.type", "publish"),
			attribute.String("messaging.destination.name", stream),
		),
	)
	if headers != nil {
		InjectHeaders(ctx, headers)
	}
	return ctx, span
}
