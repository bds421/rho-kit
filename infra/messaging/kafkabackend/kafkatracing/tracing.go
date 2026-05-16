// Package kafkatracing provides OpenTelemetry trace context propagation
// for Kafka messages. Use this in message handlers and publishers to link
// traces across service boundaries.
//
// Mirrors the [amqptracing] shape: Carrier adapts the kit's
// `map[string]string` message headers to the [TextMapCarrier]
// interface, then [InjectHeaders] / [ExtractContext] propagate W3C
// trace context. The Kafka-specific attribute set follows OTel
// semantic conventions §messaging.kafka.* for cross-vendor
// dashboards.
//
// Wave 167 adds this alongside [natstracing] and [redistracing] so
// every kit messaging backend has a consistent tracing-helper
// surface; AMQP shipped first because it was the original kit
// messaging backend.
package kafkatracing

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "kit/kafka"

// Carrier adapts a `map[string]string` (the kit's
// `messaging.Message.Headers` shape) to the [TextMapCarrier]
// interface for W3C trace context propagation through Kafka
// headers.
type Carrier map[string]string

// Get returns the value for a key, or the empty string when
// unset. Required by [TextMapCarrier].
func (c Carrier) Get(key string) string { return c[key] }

// Set stores a key-value pair. Required by [TextMapCarrier].
func (c Carrier) Set(key, value string) { c[key] = value }

// Keys returns every header name in the carrier. Required by
// [TextMapCarrier].
func (c Carrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// InjectHeaders injects trace context from ctx into Kafka message
// headers. Call before [Publisher.Publish] so the consumer side
// can link the processing trace to the producer's.
//
//	msg = msg.WithHeader("traceparent", "")  // ensure headers map exists
//	kafkatracing.InjectHeaders(ctx, msg.Headers)
//	publisher.Publish(ctx, topic, key, msg)
func InjectHeaders(ctx context.Context, headers map[string]string) {
	if headers == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, Carrier(headers))
}

// ExtractContext extracts trace context from Kafka message
// headers and returns a derived context with the parent span.
// Use in consumer handlers to link processing to the publishing
// trace.
//
//	ctx := kafkatracing.ExtractContext(ctx, delivery.Message.Headers)
func ExtractContext(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, Carrier(headers))
}

// StartConsumerSpan starts a new span for processing a Kafka
// message. Extracts trace context from headers, creates a
// [trace.SpanKindConsumer] span, and sets the OTel semantic-
// conventions attributes for Kafka.
//
// Caller MUST end the returned span. The kit convention is:
//
//	ctx, span := kafkatracing.StartConsumerSpan(ctx, headers, "process", topic, group)
//	defer span.End()
func StartConsumerSpan(ctx context.Context, headers map[string]string, operation, topic, group string) (context.Context, trace.Span) {
	ctx = ExtractContext(ctx, headers)
	tracer := otel.Tracer(tracerName)
	return tracer.Start(ctx, operation+" "+topic,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.operation.type", "process"),
			attribute.String("messaging.destination.name", topic),
			attribute.String("messaging.kafka.consumer.group", group),
		),
	)
}

// StartPublisherSpan starts a new span for publishing a Kafka
// message. Creates a [trace.SpanKindProducer] span and injects the
// W3C trace context into the supplied headers so downstream
// consumers can extract it.
//
// Caller MUST end the returned span. Typical use:
//
//	ctx, span := kafkatracing.StartPublisherSpan(ctx, msg.Headers, "publish", topic, key)
//	defer span.End()
//	if err := publisher.Publish(ctx, topic, key, msg); err != nil {
//	    span.RecordError(err)
//	    return err
//	}
func StartPublisherSpan(ctx context.Context, headers map[string]string, operation, topic, key string) (context.Context, trace.Span) {
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, operation+" "+topic,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.operation.type", "publish"),
			attribute.String("messaging.destination.name", topic),
			attribute.String("messaging.kafka.message.key", key),
		),
	)
	if headers != nil {
		InjectHeaders(ctx, headers)
	}
	return ctx, span
}
