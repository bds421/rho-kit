package amqptracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func setupTestProvider(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return recorder
}

func TestCarrier_roundTrip(t *testing.T) {
	setupTestProvider(t)

	tracer := otel.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "publish")
	defer span.End()

	headers := make(map[string]string)
	InjectHeaders(ctx, headers)

	if headers["traceparent"] == "" {
		t.Fatal("expected traceparent in headers after inject")
	}

	consumerCtx := ExtractContext(context.Background(), headers)
	extractedSpan := trace.SpanFromContext(consumerCtx)

	origTraceID := span.SpanContext().TraceID()
	extractedTraceID := extractedSpan.SpanContext().TraceID()
	if origTraceID != extractedTraceID {
		t.Errorf("trace ID mismatch: inject=%s, extract=%s", origTraceID, extractedTraceID)
	}
}

func TestCarrier_nilHeaders(t *testing.T) {
	InjectHeaders(context.Background(), nil)
	ctx := ExtractContext(context.Background(), nil)
	if ctx == nil {
		t.Error("expected non-nil context")
	}
}

func TestCarrier_keys(t *testing.T) {
	c := Carrier{
		"traceparent": "00-abc-def-01",
		"tracestate":  "foo=bar",
	}
	keys := c.Keys()
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestStartConsumerSpan(t *testing.T) {
	recorder := setupTestProvider(t)

	tracer := otel.Tracer("test")
	pubCtx, pubSpan := tracer.Start(context.Background(), "publish")
	headers := make(map[string]string)
	InjectHeaders(pubCtx, headers)
	pubSpan.End()

	ctx, consumerSpan := StartConsumerSpan(context.Background(), headers, "order.created", "events", "order.created")
	defer consumerSpan.End()

	if !trace.SpanFromContext(ctx).SpanContext().IsValid() {
		t.Error("expected valid span in consumer context")
	}

	consumerSpan.End()
	spans := recorder.Ended()

	if len(spans) < 2 {
		t.Fatalf("expected at least 2 spans, got %d", len(spans))
	}

	pubTraceID := spans[0].SpanContext().TraceID()
	consumerTraceID := spans[len(spans)-1].SpanContext().TraceID()
	if pubTraceID != consumerTraceID {
		t.Errorf("trace IDs differ: pub=%s, consumer=%s", pubTraceID, consumerTraceID)
	}
}

func TestStartPublisherSpan(t *testing.T) {
	recorder := setupTestProvider(t)

	ctx, span := StartPublisherSpan(context.Background(), "order.created", "events", "order.created")
	span.End()

	if !trace.SpanFromContext(ctx).SpanContext().IsValid() {
		t.Error("expected valid span context")
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].SpanKind() != trace.SpanKindProducer {
		t.Errorf("span kind = %v, want Producer", spans[0].SpanKind())
	}
}
