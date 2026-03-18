// Package amqptracing provides OpenTelemetry trace context propagation
// for AMQP messages. Use this in message handlers and publishers to link
// traces across service boundaries.
package amqptracing

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const amqpTracerName = "kit/amqp"

// Carrier adapts a map[string]string (messaging.Message.Headers)
// to the TextMapCarrier interface for W3C trace context propagation.
type Carrier map[string]string

// Get returns the value for a key.
func (c Carrier) Get(key string) string { return c[key] }

// Set stores a key-value pair.
func (c Carrier) Set(key, value string) { c[key] = value }

// Keys returns all keys in the carrier.
func (c Carrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// InjectHeaders injects trace context from ctx into message headers.
// Call this before publishing a message to propagate the trace across
// service boundaries.
//
//	msg = msg.WithHeader("traceparent", "")  // ensure headers map exists
//	amqptracing.InjectHeaders(ctx, msg.Headers)
//	publisher.Publish(ctx, exchange, key, msg)
func InjectHeaders(ctx context.Context, headers map[string]string) {
	if headers == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, Carrier(headers))
}

// ExtractContext extracts trace context from AMQP message headers
// and returns a new context with the parent span. Use this in message
// consumers to link processing to the publishing trace.
//
//	ctx := amqptracing.ExtractContext(ctx, delivery.Message.Headers)
func ExtractContext(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, Carrier(headers))
}

// StartConsumerSpan starts a new span for processing an AMQP message.
// It extracts trace context from headers, creates a CONSUMER span, and
// sets standard messaging attributes.
func StartConsumerSpan(ctx context.Context, headers map[string]string, operation, exchange, routingKey string) (context.Context, trace.Span) {
	ctx = ExtractContext(ctx, headers)

	tracer := otel.Tracer(amqpTracerName)
	ctx, span := tracer.Start(ctx, operation+" process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation.type", "process"),
			attribute.String("messaging.destination.name", exchange),
			attribute.String("messaging.rabbitmq.destination.routing_key", routingKey),
		),
	)
	return ctx, span
}

// StartPublisherSpan starts a new span for publishing an AMQP message.
// It creates a PRODUCER span with standard messaging attributes.
func StartPublisherSpan(ctx context.Context, operation, exchange, routingKey string) (context.Context, trace.Span) {
	tracer := otel.Tracer(amqpTracerName)
	ctx, span := tracer.Start(ctx, operation+" publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation.type", "publish"),
			attribute.String("messaging.destination.name", exchange),
			attribute.String("messaging.rabbitmq.destination.routing_key", routingKey),
		),
	)
	return ctx, span
}
