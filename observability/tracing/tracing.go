// Package tracing provides OpenTelemetry tracing setup with OTLP export.
//
// For HTTP middleware, use [middleware/tracing.HTTPMiddleware].
// For AMQP trace propagation, use [messaging/amqpbackend/amqptracing].
package tracing

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
	"go.opentelemetry.io/otel/trace"
)

// Config configures the tracing subsystem.
type Config struct {
	// ServiceName identifies this service in traces (e.g. "backend", "notification-service").
	ServiceName string

	// ServiceVersion is the service version (e.g. build tag or git SHA).
	ServiceVersion string

	// Environment is the deployment environment (e.g. "development", "production").
	Environment string

	// Endpoint is the OTLP collector endpoint (e.g. "localhost:4317").
	// Empty disables tracing (noop provider).
	Endpoint string

	// Insecure disables TLS for the OTLP gRPC connection.
	// Typically true for local collectors and development.
	Insecure bool

	// SampleRate is the fraction of traces to sample (0.0 to 1.0).
	// Default is 0.05 (5%) when unset — toolkit defaults are conservative
	// because the cost of sampling everything (CPU + collector storage)
	// surprises operators. Set to 1.0 explicitly for development.
	SampleRate float64

	// EnableBaggage enables OpenTelemetry Baggage propagation. Off by default
	// because Baggage attaches arbitrary cross-service key/value pairs to
	// every outgoing request — easy vector for accidental PII propagation
	// if any handler logs the baggage map.
	EnableBaggage bool
}

// Provider wraps a TracerProvider and its shutdown function.
type Provider struct {
	tp *sdktrace.TracerProvider
}

// Tracer returns a named tracer from this provider.
func (p *Provider) Tracer(name string) trace.Tracer {
	return p.tp.Tracer(name)
}

// Shutdown flushes pending spans and releases resources.
// Call this during graceful shutdown (with a deadline context).
func (p *Provider) Shutdown(ctx context.Context) error {
	return p.tp.Shutdown(ctx)
}

// Init initializes OpenTelemetry tracing with an OTLP gRPC exporter.
// If cfg.Endpoint is empty, a noop provider is configured (zero overhead).
// Sets the global TracerProvider and TextMapPropagator.
func Init(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Endpoint == "" {
		return initNoop()
	}

	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 0.05
	}

	res, err := buildResource(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(5*time.Second),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	// Set globals so libraries using otel.Tracer() get the real provider.
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagators(cfg.EnableBaggage))

	return &Provider{tp: tp}, nil
}

// initNoop sets up a noop tracer provider for when tracing is disabled.
func initNoop() (*Provider, error) {
	tp := sdktrace.NewTracerProvider() // no exporter = noop
	otel.SetTracerProvider(tp)
	// Even with tracing disabled, propagate W3C trace headers so downstream
	// services that DO have tracing enabled see the request graph; just
	// don't propagate Baggage by default.
	otel.SetTextMapPropagator(propagators(false))
	return &Provider{tp: tp}, nil
}

// propagators returns the configured TextMapPropagator. Baggage is opt-in
// because it carries arbitrary cross-service KVs and is easy to leak into
// logs accidentally.
func propagators(enableBaggage bool) propagation.TextMapPropagator {
	if enableBaggage {
		return propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		)
	}
	return propagation.TraceContext{}
}

func buildResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	attrs := []resource.Option{
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
		),
	}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, resource.WithAttributes(
			semconv.ServiceVersionKey.String(cfg.ServiceVersion),
		))
	}
	if cfg.Environment != "" {
		attrs = append(attrs, resource.WithAttributes(
			semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
		))
	}
	return resource.New(ctx, attrs...)
}
