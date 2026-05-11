package stack

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mwcorrelationid "github.com/bds421/rho-kit/httpx/v2/middleware/correlationid"
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

func TestDefault_OuterInnerOptionsCloneInput(t *testing.T) {
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

	outer := []func(http.Handler) http.Handler{record("outer-original")}
	inner := []func(http.Handler) http.Handler{record("inner-original")}
	outerOpt := WithOuter(outer...)
	innerOpt := WithInner(inner...)
	outer[0] = record("outer-mutated")
	inner[0] = record("inner-mutated")

	stacked := Default(handler, slog.Default(),
		WithoutMetrics(),
		WithoutRequestID(),
		WithoutCorrelationID(),
		WithoutTracing(),
		WithoutLogging(),
		outerOpt,
		innerOpt,
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	want := []string{"outer-original", "inner-original", "handler"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i, entry := range want {
		if calls[i] != entry {
			t.Fatalf("calls[%d] = %q, want %q (full: %v)", i, calls[i], entry, calls)
		}
	}
}

func TestWithQuietPathsClonesInput(t *testing.T) {
	paths := []string{"/ready"}
	opt := WithQuietPaths(paths...)
	paths[0] = "/mutated"

	var cfg Config
	opt(&cfg)

	if len(cfg.QuietPaths) != 1 || cfg.QuietPaths[0] != "/ready" {
		t.Fatalf("QuietPaths = %v, want [/ready]", cfg.QuietPaths)
	}
}

func TestDefault_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	Default(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), slog.Default(), nil)
}

func TestDefault_PanicReturns500(t *testing.T) {
	handler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom in handler")
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

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 from recover middleware", rec.Code)
	}
}

func TestDefault_PanicStillEmitsAccessLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{}))
	handler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom in handler")
	})

	stacked := Default(handler, logger,
		WithoutMetrics(),
		WithoutRequestID(),
		WithoutCorrelationID(),
		WithoutTracing(),
		WithoutRequestLogger(),
		WithoutSecHeaders(),
		WithoutTimeout(),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 from recover middleware", rec.Code)
	}
	logOutput := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("status=500")) {
		t.Fatalf("expected access log with status=500, got: %s", logOutput)
	}
	if !bytes.Contains(buf.Bytes(), []byte("panicked=true")) {
		t.Fatalf("expected access log with panicked=true, got: %s", logOutput)
	}
}

func TestDefault_PanicInsideTimeoutReturns500(t *testing.T) {
	handler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom in timeout goroutine")
	})

	stacked := Default(handler, slog.Default(),
		WithoutMetrics(),
		WithoutRequestID(),
		WithoutCorrelationID(),
		WithoutTracing(),
		WithoutLogging(),
		WithoutRequestLogger(),
		WithoutSecHeaders(),
		WithTimeout(time.Second),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 from recover middleware", rec.Code)
	}
}

func TestDefault_RecoverIsOutermost(t *testing.T) {
	outerPanic := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			panic("outer middleware exploded")
		})
	}
	innerPanic := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			panic("inner middleware exploded")
		})
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for name, opt := range map[string]Option{
		"outer": WithOuter(outerPanic),
		"inner": WithInner(innerPanic),
	} {
		t.Run(name, func(t *testing.T) {
			stacked := Default(handler, slog.Default(),
				WithoutMetrics(),
				WithoutRequestID(),
				WithoutCorrelationID(),
				WithoutTracing(),
				WithoutLogging(),
				WithoutRequestLogger(),
				WithoutSecHeaders(),
				WithoutTimeout(),
				opt,
			)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			stacked.ServeHTTP(rec, req)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (panic in %s middleware caught by recover)", rec.Code, name)
			}
		})
	}
}

func TestDefault_WithoutRecoverPropagatesPanic(t *testing.T) {
	handler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("uncaught")
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
		WithoutRecover(),
	)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic to propagate when recover is disabled")
		}
	}()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)
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

func TestWithTimeoutPanicsOnNonPositiveDuration(t *testing.T) {
	for name, d := range map[string]time.Duration{
		"zero":     0,
		"negative": -time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected WithTimeout to panic")
				}
			}()
			WithTimeout(d)
		})
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
