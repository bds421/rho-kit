// Package tracing provides OpenTelemetry tracing setup with OTLP export.
//
// For HTTP middleware, use [middleware/tracing.HTTPMiddleware].
// For AMQP trace propagation, use [messaging/amqpbackend/amqptracing].
package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
	"go.opentelemetry.io/otel/trace"
)

// defaultInitTimeout bounds the OTLP exporter handshake during [Init] when
// Config.InitTimeout is zero. Five seconds is enough to ride out a
// transient collector restart but short enough that an unreachable
// collector does not block service startup.
const defaultInitTimeout = 5 * time.Second

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

	// InitTimeout bounds the OTLP exporter handshake during [Init].
	// Default: 5 seconds. Init falls back to a noop provider with a logged
	// warning if the timeout fires, so an unreachable collector at boot
	// does not hang the service.
	//
	// Set to a negative value to disable the bound (use the caller-supplied
	// ctx as-is).
	InitTimeout time.Duration

	// OnInitFallback is invoked when Init fails to reach the collector and
	// falls back to the noop provider. If nil, the failure is logged via
	// slog.Default (level=Warn). Useful for surfacing the fallback to
	// custom telemetry (e.g. an audit log entry).
	OnInitFallback func(err error)

	// Headers are static gRPC metadata sent on every OTLP export. Use
	// for managed-collector authentication (Honeycomb, Lightstep,
	// Grafana Cloud OTLP).
	Headers map[string]string

	// Compression enables gRPC payload compression on the OTLP export.
	// Accepts "gzip" (the only standard scheme); empty disables.
	Compression string

	// BatchTimeout is the maximum time to buffer spans before exporting.
	// Default 5s. Lower this for low-traffic services where 5s of
	// buffering means most spans land on the same export tick; raise it
	// to amortise per-export overhead under heavy load.
	BatchTimeout time.Duration

	// MaxQueueSize bounds the in-memory span queue. Default 2048.
	MaxQueueSize int

	// MaxExportBatchSize caps how many spans are exported per batch.
	// Default 512.
	MaxExportBatchSize int
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
//
// The OTLP exporter handshake is bounded by cfg.InitTimeout (default
// 5 seconds). If the collector is unreachable within that window, Init
// falls back to a noop provider with a logged warning rather than
// blocking service startup. Set Config.InitTimeout < 0 to disable the
// bound and use the caller's ctx as-is.
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
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
	}
	if cfg.Compression == "gzip" {
		opts = append(opts, otlptracegrpc.WithCompressor("gzip"))
	}

	dialCtx := ctx
	var dialCancel context.CancelFunc
	switch {
	case cfg.InitTimeout < 0:
		// Disabled: use caller ctx unchanged.
	case cfg.InitTimeout == 0:
		dialCtx, dialCancel = context.WithTimeout(ctx, defaultInitTimeout)
	default:
		dialCtx, dialCancel = context.WithTimeout(ctx, cfg.InitTimeout)
	}
	if dialCancel != nil {
		defer dialCancel()
	}

	exporter, err := otlptracegrpc.New(dialCtx, opts...)
	if err != nil {
		// Don't block service startup on an unreachable collector.
		// Surface the failure and degrade to noop so the rest of the
		// stack still gets the standard propagator wired.
		if cfg.OnInitFallback != nil {
			cfg.OnInitFallback(err)
		} else {
			slog.Default().Warn("tracing: OTLP exporter dial failed; falling back to noop provider",
				"endpoint", cfg.Endpoint,
				"error", err.Error(),
			)
		}
		return initNoop()
	}

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))

	batchTimeout := cfg.BatchTimeout
	if batchTimeout <= 0 {
		batchTimeout = 5 * time.Second
	}
	batcherOpts := []sdktrace.BatchSpanProcessorOption{
		sdktrace.WithBatchTimeout(batchTimeout),
	}
	if cfg.MaxQueueSize > 0 {
		batcherOpts = append(batcherOpts, sdktrace.WithMaxQueueSize(cfg.MaxQueueSize))
	}
	if cfg.MaxExportBatchSize > 0 {
		batcherOpts = append(batcherOpts, sdktrace.WithMaxExportBatchSize(cfg.MaxExportBatchSize))
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, batcherOpts...),
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
