package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestNew_withServiceAttributes(t *testing.T) {
	logger := New(Config{
		ServiceName:    "test-svc",
		ServiceVersion: "v1.0.0",
		Environment:    "test",
	})

	if logger == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestNew_defaultLevel(t *testing.T) {
	logger := New(Config{ServiceName: "test"})

	if !logger.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("expected info level to be enabled")
	}
}

func TestNew_debugLevel(t *testing.T) {
	logger := New(Config{ServiceName: "test", Level: "debug"})

	if !logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("expected debug level to be enabled")
	}
}

func TestNew_errorLevel(t *testing.T) {
	logger := New(Config{ServiceName: "test", Level: "error"})

	if logger.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("expected info level to be disabled at error level")
	}
	if !logger.Enabled(context.Background(), slog.LevelError) {
		t.Error("expected error level to be enabled")
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelInfo},
		{"unknown", slog.LevelInfo},
	}

	for _, tt := range tests {
		got := parseLevel(tt.input)
		if got != tt.want {
			t.Errorf("parseLevel(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestWithContext_FromContext(t *testing.T) {
	logger := slog.Default().With("test_key", "test_val")
	ctx := WithContext(context.Background(), logger)

	got := FromContext(ctx)
	if got != logger {
		t.Error("FromContext should return the stored logger")
	}
}

func TestFromContext_fallsBackToDefault(t *testing.T) {
	got := FromContext(context.Background())
	if got == nil {
		t.Error("FromContext should return non-nil default logger")
	}
}

func TestFromContext_NilContextFallsBackToDefault(t *testing.T) {
	//nolint:staticcheck // Deliberately exercises the nil-safe read path.
	got := FromContext(nil)
	if got == nil {
		t.Error("FromContext should return non-nil default logger for nil context")
	}
}

func TestWithContext_NilContextUsesBackground(t *testing.T) {
	logger := slog.Default().With("test_key", "test_val")
	//nolint:staticcheck // Deliberately verifies normalization of nil context inputs.
	ctx := WithContext(nil, logger)

	if ctx == nil {
		t.Fatal("WithContext(nil, logger) returned nil")
	}
	if got := FromContext(ctx); got != logger {
		t.Error("FromContext should return the stored logger")
	}
}

func TestWithAttrs(t *testing.T) {
	logger := slog.Default()
	ctx := WithContext(context.Background(), logger)

	ctx, enriched := WithAttrs(ctx, "user_id", "abc-123")
	if enriched == nil {
		t.Fatal("expected non-nil enriched logger")
	}

	// Verify the enriched logger is stored in context.
	got := FromContext(ctx)
	if got != enriched {
		t.Error("FromContext should return the enriched logger")
	}
}

func TestWithAttrs_NilContext(t *testing.T) {
	//nolint:staticcheck // Deliberately verifies normalization of nil context inputs.
	ctx, enriched := WithAttrs(nil, "user_id", "abc-123")
	if ctx == nil {
		t.Fatal("WithAttrs(nil, ...) returned nil context")
	}
	if enriched == nil {
		t.Fatal("expected non-nil enriched logger")
	}
	if got := FromContext(ctx); got != enriched {
		t.Error("FromContext should return the enriched logger")
	}
}

func TestTraceHandler_injectsTraceID(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	handler := &traceHandler{inner: inner}
	logger := slog.New(handler)

	// Create a context with a valid span context.
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "test message")

	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("unmarshal log: %v", err)
	}

	if logEntry["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace_id = %v, want 4bf92f3577b34da6a3ce929d0e0e4736", logEntry["trace_id"])
	}
	if logEntry["span_id"] != "00f067aa0ba902b7" {
		t.Errorf("span_id = %v, want 00f067aa0ba902b7", logEntry["span_id"])
	}
}

func TestTraceHandler_noTraceContext(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	handler := &traceHandler{inner: inner}
	logger := slog.New(handler)

	logger.Info("no trace")

	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("unmarshal log: %v", err)
	}

	if _, ok := logEntry["trace_id"]; ok {
		t.Error("trace_id should not be present without trace context")
	}
}

func TestTraceHandler_withAttrs(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	handler := &traceHandler{inner: inner}

	// WithAttrs should return a traceHandler wrapping the enriched inner handler.
	enriched := handler.WithAttrs([]slog.Attr{slog.String("key", "val")})
	if _, ok := enriched.(*traceHandler); !ok {
		t.Error("WithAttrs should return a *traceHandler")
	}
}

func TestTraceHandler_withGroup(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	// Construct as New() does so root captures the group-free base handler.
	handler := &traceHandler{inner: inner, root: inner}

	grouped := handler.WithGroup("grp")
	if _, ok := grouped.(*traceHandler); !ok {
		t.Fatal("WithGroup should return a *traceHandler")
	}

	// Emit through the grouped handler with a span context and assert where the
	// trace IDs land: they must stay at the top level for log/trace correlation,
	// while the record's own attributes nest under the open group. Asserting the
	// wrapper type alone gives false confidence in this exact path.
	logger := slog.New(grouped)

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "test message", "path", "/v1/things")

	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("unmarshal log: %v", err)
	}

	if logEntry["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("top-level trace_id = %v, want 4bf92f3577b34da6a3ce929d0e0e4736", logEntry["trace_id"])
	}
	if logEntry["span_id"] != "00f067aa0ba902b7" {
		t.Errorf("top-level span_id = %v, want 00f067aa0ba902b7", logEntry["span_id"])
	}

	grp, ok := logEntry["grp"].(map[string]any)
	if !ok {
		t.Fatalf("expected grp group object, got %v", logEntry["grp"])
	}
	if grp["path"] != "/v1/things" {
		t.Errorf("grp.path = %v, want /v1/things", grp["path"])
	}
	if _, nested := grp["trace_id"]; nested {
		t.Error("trace_id should not be nested under the open group")
	}
	if _, nested := grp["span_id"]; nested {
		t.Error("span_id should not be nested under the open group")
	}
}

// TestTraceHandler_traceIDsTopLevelUnderGroup verifies that trace/log
// correlation keys stay at the top level of the record even when the logger
// has an open slog group. Pipelines key off top-level trace_id/span_id, so
// nesting them under a group (e.g. "req") silently breaks correlation.
func TestTraceHandler_traceIDsTopLevelUnderGroup(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	// Construct as New() does: root captures the group-free base handler.
	logger := slog.New(&traceHandler{inner: inner, root: inner}).WithGroup("req")

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "test message", "path", "/v1/things")

	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("unmarshal log: %v", err)
	}

	// trace_id/span_id must be at the top level for correlation.
	if logEntry["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("top-level trace_id = %v, want 4bf92f3577b34da6a3ce929d0e0e4736", logEntry["trace_id"])
	}
	if logEntry["span_id"] != "00f067aa0ba902b7" {
		t.Errorf("top-level span_id = %v, want 00f067aa0ba902b7", logEntry["span_id"])
	}

	// The user's own attribute must still be nested under the open group.
	grp, ok := logEntry["req"].(map[string]any)
	if !ok {
		t.Fatalf("expected req group object, got %v", logEntry["req"])
	}
	if grp["path"] != "/v1/things" {
		t.Errorf("req.path = %v, want /v1/things", grp["path"])
	}
	// The trace IDs must NOT be nested under the group.
	if _, nested := grp["trace_id"]; nested {
		t.Error("trace_id should not be nested under the open group")
	}
	if _, nested := grp["span_id"]; nested {
		t.Error("span_id should not be nested under the open group")
	}
}

// TestTraceHandler_traceIDsWithAttrsNoGroup verifies that attributes applied
// via WithAttrs (no open group) continue to appear correctly alongside the
// injected trace IDs at the top level.
func TestTraceHandler_traceIDsWithAttrsNoGroup(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	logger := slog.New(&traceHandler{inner: inner}).With("service.name", "svc")

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "test message")

	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("unmarshal log: %v", err)
	}

	if logEntry["service.name"] != "svc" {
		t.Errorf("service.name = %v, want svc", logEntry["service.name"])
	}
	if logEntry["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace_id = %v, want 4bf92f3577b34da6a3ce929d0e0e4736", logEntry["trace_id"])
	}
	if logEntry["span_id"] != "00f067aa0ba902b7" {
		t.Errorf("span_id = %v, want 00f067aa0ba902b7", logEntry["span_id"])
	}

	// Sanity: trace IDs must be top-level, not nested under any group.
	if _, ok := logEntry["service.name"].(map[string]any); ok {
		t.Fatal("service.name should be a scalar, not a group")
	}
}

// TestTraceHandler_nestedGroupsAndHandlerAttrs verifies the chain replay keeps
// trace IDs at the top level while preserving nested groups and handler-level
// attributes added both before and after the group is opened.
func TestTraceHandler_nestedGroupsAndHandlerAttrs(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	logger := slog.New(&traceHandler{inner: inner, root: inner}).
		With("service.name", "svc").
		WithGroup("req").
		With("rid", "r-1").
		WithGroup("inner")

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "msg", "field", "v")

	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("unmarshal log: %v", err)
	}

	if logEntry["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("top-level trace_id = %v, want set", logEntry["trace_id"])
	}
	if logEntry["span_id"] != "00f067aa0ba902b7" {
		t.Errorf("top-level span_id = %v, want set", logEntry["span_id"])
	}
	// service.name was added before any group, so it stays top-level.
	if logEntry["service.name"] != "svc" {
		t.Errorf("service.name = %v, want svc", logEntry["service.name"])
	}

	req, ok := logEntry["req"].(map[string]any)
	if !ok {
		t.Fatalf("expected req group, got %v", logEntry["req"])
	}
	if req["rid"] != "r-1" {
		t.Errorf("req.rid = %v, want r-1", req["rid"])
	}
	innerGrp, ok := req["inner"].(map[string]any)
	if !ok {
		t.Fatalf("expected req.inner group, got %v", req["inner"])
	}
	if innerGrp["field"] != "v" {
		t.Errorf("req.inner.field = %v, want v", innerGrp["field"])
	}
	// Trace IDs must not leak into any nested group.
	if _, nested := req["trace_id"]; nested {
		t.Error("trace_id leaked into req group")
	}
	if _, nested := innerGrp["trace_id"]; nested {
		t.Error("trace_id leaked into req.inner group")
	}
}

// TestNew_traceIDsTopLevelUnderGroupE2E exercises the public New() API end to
// end: a grouped logger from New(...) must still emit trace_id/span_id at the
// top level of the JSON record written to stdout.
func TestNew_traceIDsTopLevelUnderGroupE2E(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	logger := New(Config{ServiceName: "svc"}).WithGroup("req")

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "msg", "path", "/v1")

	if cerr := w.Close(); cerr != nil {
		t.Fatalf("close write pipe: %v", cerr)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	var logEntry map[string]any
	if err := json.Unmarshal(out, &logEntry); err != nil {
		t.Fatalf("unmarshal log %q: %v", string(out), err)
	}

	if logEntry["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("top-level trace_id = %v, want 4bf92f3577b34da6a3ce929d0e0e4736", logEntry["trace_id"])
	}
	if logEntry["span_id"] != "00f067aa0ba902b7" {
		t.Errorf("top-level span_id = %v, want 00f067aa0ba902b7", logEntry["span_id"])
	}
	// service.name added by New() stays top-level (pre-group).
	if logEntry["service.name"] != "svc" {
		t.Errorf("service.name = %v, want svc", logEntry["service.name"])
	}
	req, ok := logEntry["req"].(map[string]any)
	if !ok {
		t.Fatalf("expected req group, got %v", logEntry["req"])
	}
	if req["path"] != "/v1" {
		t.Errorf("req.path = %v, want /v1", req["path"])
	}
	if _, nested := req["trace_id"]; nested {
		t.Error("trace_id should not be nested under req")
	}
}
