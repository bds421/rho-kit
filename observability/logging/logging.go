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

	// Wrap with trace-injecting handler. root captures the group-free base
	// handler so trace_id/span_id are always injected at the top level, even
	// after consumers open a slog group via WithGroup.
	traceHandler := &traceHandler{inner: handler, root: handler}

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
//
// trace_id/span_id are always emitted at the top level of the record so that
// log pipelines can correlate logs with traces by a stable key. slog applies
// any open WithGroup qualifier to attrs added during Handle, which would
// otherwise nest the IDs under that group (e.g. "req"). To keep the IDs at the
// top level, the handler tracks the open-group/attr chain and, when a group is
// open, replays it onto root (the group-free base handler) with the IDs added
// outside the group.
type traceHandler struct {
	// inner is the wrapped handler with all WithAttrs/WithGroup applied. It is
	// used for the common, group-free path.
	inner slog.Handler

	// root is the group-free base handler used to emit trace IDs at the top
	// level when a group is open. nil for legacy direct construction, in which
	// case the group-free fast path is used.
	root slog.Handler

	// ops records the WithAttrs/WithGroup chain in order, so it can be replayed
	// onto root with the trace IDs kept outside any open group.
	ops []handlerOp
}

// handlerOp is one step of the WithAttrs/WithGroup chain. A non-empty group
// opens a nested group; otherwise attrs are appended at the current level.
type handlerOp struct {
	group string
	attrs []slog.Attr
}

// hasGroup reports whether any WithGroup is currently open in the chain.
func (h *traceHandler) hasGroup() bool {
	for _, op := range h.ops {
		if op.group != "" {
			return true
		}
	}
	return false
}

func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *traceHandler) Handle(ctx context.Context, record slog.Record) error {
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return h.inner.Handle(ctx, record)
	}

	idAttrs := []slog.Attr{
		slog.String("trace_id", spanCtx.TraceID().String()),
		slog.String("span_id", spanCtx.SpanID().String()),
	}

	// Fast path: no open group (or legacy direct construction). Adding the IDs
	// to the record keeps them at the top level.
	if h.root == nil || !h.hasGroup() {
		record.AddAttrs(idAttrs...)
		return h.inner.Handle(ctx, record)
	}

	// A group is open: replay the WithAttrs/WithGroup chain onto the group-free
	// root so the trace IDs stay at the top level while the record's own attrs
	// remain nested under the open group(s).
	recAttrs := make([]slog.Attr, 0, record.NumAttrs())
	record.Attrs(func(a slog.Attr) bool {
		recAttrs = append(recAttrs, a)
		return true
	})

	replay := h.replayChain(recAttrs)

	out := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	out.AddAttrs(idAttrs...)
	out.AddAttrs(replay...)
	return h.root.Handle(ctx, out)
}

// replayChain reconstructs the WithAttrs/WithGroup chain as a flat list of
// top-level attrs, nesting grouped attrs under slog.Group values. recAttrs are
// the record's own attributes, placed at the innermost (most-nested) level.
func (h *traceHandler) replayChain(recAttrs []slog.Attr) []slog.Attr {
	// Walk the chain backwards, folding each op into the accumulator. A group
	// op wraps the current accumulator into a single nested group attr.
	acc := recAttrs
	for i := len(h.ops) - 1; i >= 0; i-- {
		op := h.ops[i]
		if op.group != "" {
			acc = []slog.Attr{{Key: op.group, Value: slog.GroupValue(acc...)}}
			continue
		}
		// WithAttrs at this level: prepend so order matches slog (handler attrs
		// before record attrs).
		acc = append(append([]slog.Attr{}, op.attrs...), acc...)
	}
	return acc
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return &traceHandler{
		inner: h.inner.WithAttrs(attrs),
		root:  h.root,
		ops:   appendOp(h.ops, handlerOp{attrs: attrs}),
	}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &traceHandler{
		inner: h.inner.WithGroup(name),
		root:  h.root,
		ops:   appendOp(h.ops, handlerOp{group: name}),
	}
}

// appendOp returns a new slice with op appended, copying the existing ops so
// derived handlers never share backing storage.
func appendOp(ops []handlerOp, op handlerOp) []handlerOp {
	out := make([]handlerOp, len(ops), len(ops)+1)
	copy(out, ops)
	return append(out, op)
}
