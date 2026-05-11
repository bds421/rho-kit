// Package logging provides structured logging conventions built on slog.
// It enriches loggers with service identity (name, version, environment),
// supports context-based logger propagation for request-scoped attributes,
// and bridges with OpenTelemetry to inject trace/span IDs into log output.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// contextKey is the context key for storing *slog.Logger values.
// Unexported to prevent external packages from bypassing WithContext/FromContext.
type contextKey struct{}

// Config controls logger creation.
type Config struct {
	// Level is the minimum log level. Accepted: "debug", "info", "warn", "error".
	// Default: "info".
	Level string

	// ServiceName identifies the service (e.g. "backend", "notification-service").
	ServiceName string

	// ServiceVersion is the build version or git SHA.
	ServiceVersion string

	// Environment is the deployment environment (e.g. "development", "production").
	Environment string
}

// New creates a JSON slog.Logger enriched with service identity attributes.
// The logger writes to stdout and is suitable for container log aggregation.
func New(cfg Config) *slog.Logger {
	level := parseLevel(cfg.Level)

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})

	// Wrap with trace-injecting handler.
	traceHandler := &traceHandler{inner: handler}

	logger := slog.New(traceHandler)

	// Add service attributes using OpenTelemetry semantic-convention keys
	// (service.name, service.version, deployment.environment.name) so log
	// data correlates with traces emitted by observability/tracing without
	// the operator having to remap fields in their pipeline.
	if cfg.ServiceName != "" {
		logger = logger.With("service.name", cfg.ServiceName)
	}
	if cfg.ServiceVersion != "" {
		logger = logger.With("service.version", cfg.ServiceVersion)
	}
	if cfg.Environment != "" {
		logger = logger.With("deployment.environment.name", cfg.Environment)
	}

	return logger
}

// WithContext stores the logger in the context. Retrieve with FromContext.
//
// FR-084 [LOW]: nil loggers are normalised to slog.Default() so a
// caller who accidentally stores nil cannot trigger a request-time
// nil-deref on a downstream FromContext().With(...).
func WithContext(ctx context.Context, logger *slog.Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return context.WithValue(ctx, contextKey{}, logger)
}

// FromContext retrieves the logger from the context. Returns slog.Default()
// if no logger was stored or if a nil was stored (FR-084).
func FromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.Default()
	}
	if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// WithAttrs returns a new logger with additional attributes, stored in the context.
// Useful for enriching the logger with request-scoped values (user ID, request ID).
func WithAttrs(ctx context.Context, attrs ...any) (context.Context, *slog.Logger) {
	logger := FromContext(ctx).With(attrs...)
	return WithContext(ctx, logger), logger
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// traceHandler wraps an slog.Handler to inject OpenTelemetry trace and span
// IDs from the context into every log record.
type traceHandler struct {
	inner slog.Handler
}

func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *traceHandler) Handle(ctx context.Context, record slog.Record) error {
	spanCtx := trace.SpanContextFromContext(ctx)
	if spanCtx.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", spanCtx.TraceID().String()),
			slog.String("span_id", spanCtx.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, record)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{inner: h.inner.WithGroup(name)}
}
