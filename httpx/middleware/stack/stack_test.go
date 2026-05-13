package stack

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mwauditlog "github.com/bds421/rho-kit/httpx/v2/middleware/auditlog"
	mwcorrelationid "github.com/bds421/rho-kit/httpx/v2/middleware/correlationid"
	"github.com/bds421/rho-kit/observability/v2/auditlog"
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

// auditRecordingStore captures audit events emitted by the middleware.
type auditRecordingStore struct {
	events []auditlog.Event
}

func (s *auditRecordingStore) Append(_ context.Context, e auditlog.Event) error {
	s.events = append(s.events, e)
	return nil
}

func (s *auditRecordingStore) AppendChained(_ context.Context, build func(prev []byte) (auditlog.Event, error)) error {
	var prev []byte
	if len(s.events) > 0 {
		tail := s.events[len(s.events)-1].HMAC
		if len(tail) > 0 {
			prev = append([]byte(nil), tail...)
		}
	}
	event, err := build(prev)
	if err != nil {
		return err
	}
	s.events = append(s.events, event)
	return nil
}

func (s *auditRecordingStore) Query(_ context.Context, _ auditlog.Filter, _ string, _ int) ([]auditlog.Event, string, error) {
	return nil, "", nil
}

func (s *auditRecordingStore) LastHMAC(_ context.Context) ([]byte, error) {
	if len(s.events) == 0 {
		return nil, nil
	}
	return s.events[len(s.events)-1].HMAC, nil
}

func newAuditLogger() (*auditRecordingStore, *auditlog.Logger) {
	store := &auditRecordingStore{}
	key := bytes.Repeat([]byte{0xab}, 32)
	return store, auditlog.New(store,
		auditlog.WithChainKey(key),
		auditlog.WithCursorKey(key),
	)
}

func TestDefault_WithAuditLog_EmitsEventPerRequest(t *testing.T) {
	store, l := newAuditLogger()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
		WithAuditLog(l, mwauditlog.WithActorExtractor(func(_ *http.Request) string { return "alice@example.com" })),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/widgets", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(store.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(store.events))
	}
	ev := store.events[0]
	if ev.Actor != "alice@example.com" {
		t.Errorf("Actor = %q, want alice@example.com", ev.Actor)
	}
	if ev.Action != http.MethodGet {
		t.Errorf("Action = %q, want %s", ev.Action, http.MethodGet)
	}
	if ev.Resource != "/api/widgets" {
		t.Errorf("Resource = %q, want /api/widgets", ev.Resource)
	}
	if ev.Status != "success" {
		t.Errorf("Status = %q, want success", ev.Status)
	}
}

func TestDefault_WithoutAuditLog_NoEvents(t *testing.T) {
	// Sanity-check the omission documented in the godoc: Default does NOT
	// wire the audit-log middleware on its own. A test that constructed an
	// audit logger but never passed WithAuditLog must produce zero events.
	store, _ := newAuditLogger()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

	req := httptest.NewRequest(http.MethodGet, "/api/widgets", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if len(store.events) != 0 {
		t.Fatalf("audit events = %d, want 0 (Default must not wire auditlog)", len(store.events))
	}
}

func TestDefault_WithAuditLog_InnerWrapsAudit(t *testing.T) {
	// Stack ordering invariant: WithInner middleware (typically auth) must run
	// OUTSIDE the audit middleware so the audit entry captures the
	// authenticated actor. The recorded order should be: inner enters →
	// audit enters → handler runs → audit exits → inner exits.
	store, l := newAuditLogger()
	var order []string
	innerMW := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name+":enter")
				next.ServeHTTP(w, r)
				order = append(order, name+":exit")
			})
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		order = append(order, "handler")
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
		WithInner(innerMW("auth")),
		WithAuditLog(l),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/widgets", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if len(order) < 3 {
		t.Fatalf("order = %v, want at least 3 entries", order)
	}
	if order[0] != "auth:enter" {
		t.Errorf("first entry = %q, want auth:enter (inner must wrap audit)", order[0])
	}
	if order[len(order)-1] != "auth:exit" {
		t.Errorf("last entry = %q, want auth:exit", order[len(order)-1])
	}
	if len(store.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(store.events))
	}
}

func TestWithAuditLog_PanicsOnNilLogger(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected WithAuditLog to panic on nil logger")
		}
	}()
	WithAuditLog(nil)
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
