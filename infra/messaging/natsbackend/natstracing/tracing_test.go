package natstracing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/bds421/rho-kit/infra/messaging/natsbackend/v2/natstracing"
)

func installPropagator(t *testing.T) {
	t.Helper()
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })
}

func TestInjectExtract_RoundTrips(t *testing.T) {
	installPropagator(t)
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	tracer := otel.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "producer")
	defer span.End()
	want := span.SpanContext()

	headers := map[string]string{}
	natstracing.InjectHeaders(ctx, headers)
	require.NotEmpty(t, headers)

	consumerCtx := natstracing.ExtractContext(context.Background(), headers)
	got := trace.SpanContextFromContext(consumerCtx)
	require.True(t, got.IsValid())
	assert.Equal(t, want.TraceID(), got.TraceID())
}

func TestCarrier_OperationsRoundTrip(t *testing.T) {
	c := natstracing.Carrier{}
	c.Set("k", "v")
	assert.Equal(t, "v", c.Get("k"))
	assert.Empty(t, c.Get("missing"))
	assert.Equal(t, []string{"k"}, c.Keys())
}

func TestStartPublisherSpan_PopulatesHeaders(t *testing.T) {
	installPropagator(t)
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	headers := map[string]string{}
	_, span := natstracing.StartPublisherSpan(context.Background(), headers, "publish", "orders.created")
	defer span.End()
	assert.NotEmpty(t, headers)
}

func TestNilHeadersIsNoOp(t *testing.T) {
	installPropagator(t)
	natstracing.InjectHeaders(context.Background(), nil)
}
