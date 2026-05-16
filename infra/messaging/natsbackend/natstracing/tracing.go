// Package natstracing provides OpenTelemetry trace context propagation
// for NATS JetStream messages. Mirrors [amqptracing] and
// [kafkatracing]: Carrier adapts the kit's `map[string]string`
// message headers to the [TextMapCarrier] interface, with span
// helpers that set OTel semantic-conventions attributes for NATS.
package natstracing

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "kit/nats"

// Carrier adapts `map[string]string` (the kit's
// `messaging.Message.Headers`) to the [TextMapCarrier] interface for
// W3C trace context propagation through NATS headers.
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

// InjectHeaders injects trace context from ctx into NATS message
// headers. Call before publishing.
func InjectHeaders(ctx context.Context, headers map[string]string) {
	if headers == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, Carrier(headers))
}

// ExtractContext extracts trace context from NATS message headers
// and returns a derived context with the parent span.
func ExtractContext(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, Carrier(headers))
}

// StartConsumerSpan starts a [trace.SpanKindConsumer] span for
// processing a NATS JetStream message. The (stream, durable)
// coordinates land on `messaging.destination.name` and
// `messaging.nats.consumer.durable_name`.
func StartConsumerSpan(ctx context.Context, headers map[string]string, operation, stream, durable string) (context.Context, trace.Span) {
	ctx = ExtractContext(ctx, headers)
	tracer := otel.Tracer(tracerName)
	return tracer.Start(ctx, operation+" "+stream,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.operation.type", "process"),
			attribute.String("messaging.destination.name", stream),
			attribute.String("messaging.nats.consumer.durable_name", durable),
		),
	)
}

// StartPublisherSpan starts a [trace.SpanKindProducer] span for
// publishing a NATS JetStream message. Injects the W3C trace
// context into the supplied headers before returning.
func StartPublisherSpan(ctx context.Context, headers map[string]string, operation, subject string) (context.Context, trace.Span) {
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, operation+" "+subject,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.operation.type", "publish"),
			attribute.String("messaging.destination.name", subject),
		),
	)
	if headers != nil {
		InjectHeaders(ctx, headers)
	}
	return ctx, span
}
