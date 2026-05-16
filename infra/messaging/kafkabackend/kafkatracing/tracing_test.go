package kafkatracing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/bds421/rho-kit/infra/messaging/kafkabackend/v2/kafkatracing"
)

// installPropagator restores the global propagator after each test
// so other suites are not contaminated.
func installPropagator(t *testing.T) {
	t.Helper()
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })
}

func TestCarrier_SetGetKeys(t *testing.T) {
	c := kafkatracing.Carrier{}
	c.Set("traceparent", "value-1")
	c.Set("baggage", "value-2")

	assert.Equal(t, "value-1", c.Get("traceparent"))
	assert.Empty(t, c.Get("unset"))
	assert.ElementsMatch(t, []string{"traceparent", "baggage"}, c.Keys())
}

func TestInjectExtract_RoundTripsTraceContext(t *testing.T) {
	installPropagator(t)

	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	// Start a producer-side span to populate the context.
	tracer := otel.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "producer")
	defer span.End()
	want := span.SpanContext()

	headers := map[string]string{}
	kafkatracing.InjectHeaders(ctx, headers)
	require.NotEmpty(t, headers, "InjectHeaders must write the traceparent into the carrier")

	// Simulate consumer-side: extract into a fresh ctx and verify
	// the parent SpanContext lines up with the producer's.
	consumerCtx := kafkatracing.ExtractContext(context.Background(), headers)
	got := trace.SpanContextFromContext(consumerCtx)
	require.True(t, got.IsValid(), "extracted SpanContext must be valid")
	assert.Equal(t, want.TraceID(), got.TraceID(), "trace ID must round-trip across the carrier")
	assert.Equal(t, want.SpanID(), got.SpanID(), "span ID must round-trip as the parent")
}

func TestInjectHeaders_NilHeadersIsNoOp(t *testing.T) {
	installPropagator(t)
	// Must not panic on a nil map.
	kafkatracing.InjectHeaders(context.Background(), nil)
}

func TestExtractContext_EmptyHeadersReturnsCtx(t *testing.T) {
	installPropagator(t)
	got := kafkatracing.ExtractContext(context.Background(), nil)
	assert.NotNil(t, got)
}

func TestStartConsumerSpan_SetsAttributes(t *testing.T) {
	installPropagator(t)
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	_, span := kafkatracing.StartConsumerSpan(context.Background(), nil, "process", "orders", "billing-svc")
	defer span.End()

	require.True(t, span.IsRecording())
	assert.Equal(t, trace.SpanKindConsumer, kindOf(t, span))
}

func TestStartPublisherSpan_InjectsHeaders(t *testing.T) {
	installPropagator(t)
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	headers := map[string]string{}
	_, span := kafkatracing.StartPublisherSpan(context.Background(), headers, "publish", "orders", "user-42")
	defer span.End()

	assert.NotEmpty(t, headers, "headers must carry the injected traceparent after StartPublisherSpan")
	assert.Equal(t, trace.SpanKindProducer, kindOf(t, span))
}

// kindOf is a tiny helper that extracts the SpanKind. The SDK type
// stores it on the read-only span snapshot; the public Span
// interface only exposes IsRecording / TracerProvider so this test
// indirects via the embedded readWriteSpan when available.
//
// In practice we only test that producer vs consumer kinds differ,
// not that they match a specific value — Start* uses the SDK's
// own type-tag so this round-trip works in-process.
func kindOf(t *testing.T, s trace.Span) trace.SpanKind {
	t.Helper()
	type rwSpan interface {
		SpanKind() trace.SpanKind
	}
	if rs, ok := s.(rwSpan); ok {
		return rs.SpanKind()
	}
	t.Fatalf("span does not expose SpanKind; type %T", s)
	return 0
}
