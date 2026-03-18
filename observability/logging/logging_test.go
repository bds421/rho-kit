package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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
	handler := &traceHandler{inner: inner}

	grouped := handler.WithGroup("grp")
	if _, ok := grouped.(*traceHandler); !ok {
		t.Error("WithGroup should return a *traceHandler")
	}
}
