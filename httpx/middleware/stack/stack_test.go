package stack

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mwcorrelationid "github.com/bds421/rho-kit/httpx/middleware/correlationid"
)

func TestDefault_OrderWithOuterInner(t *testing.T) {
	var calls []string

	record := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "handler")
	})

	stacked := Default(handler, slog.Default(),
		WithoutMetrics(),
		WithoutRequestID(),
		WithoutCorrelationID(),
		WithoutTracing(),
		WithoutLogging(),
		WithOuter(record("outer1"), record("outer2")),
		WithInner(record("inner1"), record("inner2")),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	want := []string{"outer1", "outer2", "inner1", "inner2", "handler"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i, entry := range want {
		if calls[i] != entry {
			t.Fatalf("calls[%d] = %q, want %q (full: %v)", i, calls[i], entry, calls)
		}
	}
}

func TestDefault_TimeoutFiresOnSlowHandler(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			// honour cancellation so the middleware can return promptly
		}
	})

	stacked := Default(handler, slog.Default(),
		WithoutMetrics(),
		WithoutRequestID(),
		WithoutCorrelationID(),
		WithoutTracing(),
		WithoutLogging(),
		WithoutRequestLogger(),
		WithoutSecHeaders(),
		WithTimeout(20*time.Millisecond),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 from timeout, got %d", rec.Code)
	}
}

func TestDefault_WithoutTimeoutAllowsSlowHandler(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	stacked := Default(handler, slog.Default(),
		WithoutMetrics(),
		WithoutRequestID(),
		WithoutCorrelationID(),
		WithoutTracing(),
		WithoutLogging(),
		WithoutRequestLogger(),
		WithoutSecHeaders(),
		WithoutTimeout(),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with timeout disabled, got %d", rec.Code)
	}
}

func TestDefault_WithoutCorrelationID(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	stacked := Default(handler, slog.Default(),
		WithoutMetrics(),
		WithoutRequestID(),
		WithoutTracing(),
		WithoutLogging(),
		WithoutRequestLogger(),
		WithoutSecHeaders(),
		WithoutCorrelationID(),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if got := rec.Header().Get(mwcorrelationid.Header); got != "" {
		t.Errorf("expected no %s header when correlation ID disabled, got %q", mwcorrelationid.Header, got)
	}
}
