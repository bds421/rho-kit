package recover

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/bds421/rho-kit/core/contextutil"
)

func newCapturingLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

func panicHandler(v any) http.Handler {
	return http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic(v)
	})
}

func TestMiddleware_Recovers500(t *testing.T) {
	logger, buf := newCapturingLogger()

	mw := Middleware(WithLogger(logger))
	handler := mw(panicHandler("kaboom"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"INTERNAL"`) {
		t.Errorf("body missing INTERNAL code: %q", body)
	}
	if !strings.Contains(buf.String(), "panic recovered") {
		t.Errorf("expected panic-recovered log entry, got: %q", buf.String())
	}
}

func TestMiddleware_IncludesRequestID(t *testing.T) {
	logger, buf := newCapturingLogger()
	mw := Middleware(WithLogger(logger))
	handler := mw(panicHandler("oops"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = req.WithContext(contextutil.SetRequestID(context.Background(), "rid-abc-123"))
	handler.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), `"request_id":"rid-abc-123"`) {
		t.Errorf("body missing request_id: %q", rec.Body.String())
	}
	if !strings.Contains(buf.String(), `"request_id":"rid-abc-123"`) {
		t.Errorf("log missing request_id: %q", buf.String())
	}
}

func TestMiddleware_AbortHandlerNotRecovered(t *testing.T) {
	logger, buf := newCapturingLogger()
	mw := Middleware(WithLogger(logger))
	handler := mw(panicHandler(http.ErrAbortHandler))

	defer func() {
		rv := recover() //nolint:predeclared // see recover.go for explanation
		if rv == nil {
			t.Fatal("ErrAbortHandler should re-panic so net/http's outer recover sees it")
		}
		if rv != http.ErrAbortHandler {
			t.Fatalf("re-raised value = %v, want ErrAbortHandler", rv)
		}
		if buf.Len() != 0 {
			t.Errorf("ErrAbortHandler should not log; got: %q", buf.String())
		}
	}()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	handler.ServeHTTP(rec, req)
}

func TestMiddleware_PanicAfterWriteHeaderLogsButDoesNotDoubleWrite(t *testing.T) {
	logger, buf := newCapturingLogger()
	mw := Middleware(WithLogger(logger))

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"already":"sent"}`))
		panic("oops — too late")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Errorf("expected 418 (status was already sent), got %d", rec.Code)
	}
	if !strings.Contains(buf.String(), "panic after response started") {
		t.Errorf("expected response-started warning, got: %q", buf.String())
	}
	if strings.Contains(rec.Body.String(), `"INTERNAL"`) {
		t.Errorf("must not write the recovery JSON body when response already started; body = %q", rec.Body.String())
	}
}

func TestMiddleware_MetricsCounterIncrements(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	mw := Middleware(WithLogger(slog.New(slog.NewJSONHandler(io.Discard, nil))), WithMetrics(metrics))
	handler := mw(panicHandler("counted"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	handler.ServeHTTP(rec, req)

	count := readCounter(t, metrics.panics.WithLabelValues(http.MethodPost))
	if count != 1 {
		t.Errorf("panic counter = %v, want 1", count)
	}
}

func TestMiddleware_OutermostCatchesPanicInInnerMiddleware(t *testing.T) {
	logger, buf := newCapturingLogger()

	innerMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			panic("inner middleware exploded")
		})
	}

	chain := Middleware(WithLogger(logger))(innerMW(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(buf.String(), "inner middleware exploded") {
		t.Errorf("expected panic value in log; got: %q", buf.String())
	}
}

func TestMiddleware_CustomBody(t *testing.T) {
	mw := Middleware(
		WithLogger(slog.New(slog.NewJSONHandler(io.Discard, nil))),
		WithBody(func(_ *http.Request, pv any) []byte {
			return []byte(`{"custom":"` + pv.(string) + `"}`)
		}),
	)
	handler := mw(panicHandler("zzz"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	handler.ServeHTTP(rec, req)

	if got := rec.Body.String(); got != `{"custom":"zzz"}` {
		t.Errorf("body = %q, want custom builder output", got)
	}
}

func TestMiddleware_CustomStatusCode(t *testing.T) {
	mw := Middleware(
		WithLogger(slog.New(slog.NewJSONHandler(io.Discard, nil))),
		WithStatusCode(http.StatusBadGateway),
	)
	handler := mw(panicHandler("x"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestMiddleware_NoPanicNoRecovery(t *testing.T) {
	logger, buf := newCapturingLogger()
	mw := Middleware(WithLogger(logger))

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("happy path broke: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if buf.Len() != 0 {
		t.Errorf("logger received output without a panic: %q", buf.String())
	}
}

func readCounter(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("counter Write: %v", err)
	}
	return m.GetCounter().GetValue()
}
