package stack

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
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
