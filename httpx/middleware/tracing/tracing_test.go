package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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

func TestHTTPMiddleware_recordsSpanStatusAndRepanicsWhenHandlerPanics(t *testing.T) {
	recorder := setupTestProvider(t)
	handlerPanic := assertPanicValue{}

	handler := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(handlerPanic)
	}))

	defer func() {
		got := recover()
		if got != handlerPanic {
			t.Fatalf("panic = %#v, want %#v", got, handlerPanic)
		}
		spans := recorder.Ended()
		if len(spans) != 1 {
			t.Fatalf("expected 1 span, got %d", len(spans))
		}
		if spans[0].Name() != "GET /panic" {
			t.Fatalf("span name = %q, want route pattern", spans[0].Name())
		}
		if spans[0].Status().Code != codes.Error {
			t.Fatalf("span status = %v, want Error", spans[0].Status().Code)
		}
		if got := intAttribute(spans[0].Attributes(), "http.response.status_code"); got != http.StatusInternalServerError {
			t.Fatalf("status attr = %d, want 500", got)
		}
	}()

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	req.Pattern = "/panic"
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

func TestHTTPMiddleware_PanicAfterHeaderKeepsWireStatusButMarksError(t *testing.T) {
	recorder := setupTestProvider(t)
	handlerPanic := assertPanicValue{}

	handler := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		panic(handlerPanic)
	}))

	defer func() {
		got := recover()
		if got != handlerPanic {
			t.Fatalf("panic = %#v, want %#v", got, handlerPanic)
		}
		spans := recorder.Ended()
		if len(spans) != 1 {
			t.Fatalf("expected 1 span, got %d", len(spans))
		}
		if spans[0].Status().Code != codes.Error {
			t.Fatalf("span status = %v, want Error", spans[0].Status().Code)
		}
		if got := intAttribute(spans[0].Attributes(), "http.response.status_code"); got != http.StatusCreated {
			t.Fatalf("status attr = %d, want 201", got)
		}
	}()

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/created", nil))
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

func TestInjectHTTPHeaders_NilRequestNoops(t *testing.T) {
	setupTestProvider(t)

	tracer := otel.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "outgoing-call")
	defer span.End()

	InjectHTTPHeaders(ctx, nil)
}

type assertPanicValue struct{}

func intAttribute(attrs []attribute.KeyValue, key string) int {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return int(attr.Value.AsInt64())
		}
	}
	return 0
}
