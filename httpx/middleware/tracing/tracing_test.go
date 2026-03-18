package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
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

func TestHTTPMiddleware_createsSpan(t *testing.T) {
	recorder := setupTestProvider(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())
		if !span.SpanContext().IsValid() {
			t.Error("expected valid span in request context")
		}
		w.WriteHeader(http.StatusOK)
	})

	handler := HTTPMiddleware(inner)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	handler.ServeHTTP(w, r)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	span := spans[0]
	if span.SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want Server", span.SpanKind())
	}
}

func TestHTTPMiddleware_propagatesTraceContext(t *testing.T) {
	recorder := setupTestProvider(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := HTTPMiddleware(inner)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	r.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	handler.ServeHTTP(w, r)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	traceID := spans[0].SpanContext().TraceID().String()
	if traceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace ID = %s, want parent trace ID", traceID)
	}

	if w.Header().Get("traceparent") == "" {
		t.Error("expected traceparent in response headers")
	}
}

func TestHTTPMiddleware_setsErrorStatusOn5xx(t *testing.T) {
	recorder := setupTestProvider(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	handler := HTTPMiddleware(inner)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/fail", nil)
	handler.ServeHTTP(w, r)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	if spans[0].Status().Code != codes.Error {
		t.Errorf("span status = %v, want Error", spans[0].Status().Code)
	}
}

func TestInjectHTTPHeaders(t *testing.T) {
	setupTestProvider(t)

	tracer := otel.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "outgoing-call")
	defer span.End()

	req := httptest.NewRequest(http.MethodGet, "/downstream", nil)
	InjectHTTPHeaders(ctx, req)

	if req.Header.Get("traceparent") == "" {
		t.Error("expected traceparent header after inject")
	}
}
