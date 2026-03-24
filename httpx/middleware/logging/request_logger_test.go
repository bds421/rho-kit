package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bds421/rho-kit/httpx"
)

func TestWithRequestLogger_InjectsLogger(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var got *slog.Logger
	handler := WithRequestLogger(base)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = httpx.Logger(r.Context(), nil)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got == nil {
		t.Fatal("expected logger in context, got nil")
	}

	// Log a message so the buffered handler emits output we can inspect.
	got.Info("probe")

	output := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("method=GET")) {
		t.Errorf("expected method=GET in logger attrs, got: %s", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte("path=/api/items")) {
		t.Errorf("expected path=/api/items in logger attrs, got: %s", output)
	}
}

func TestWithRequestLogger_InjectsLogger_WithRequestID(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var got *slog.Logger
	handler := WithRequestLogger(base)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = httpx.Logger(r.Context(), nil)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/orders", nil)
	ctx := httpx.SetRequestID(req.Context(), "req-abc-123")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got == nil {
		t.Fatal("expected logger in context, got nil")
	}

	got.Info("probe")

	output := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("request_id=req-abc-123")) {
		t.Errorf("expected request_id=req-abc-123 in logger attrs, got: %s", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte("method=POST")) {
		t.Errorf("expected method=POST in logger attrs, got: %s", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte("path=/api/orders")) {
		t.Errorf("expected path=/api/orders in logger attrs, got: %s", output)
	}
}

func TestWithRequestLogger_NoRequestID_OmitsAttr(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var got *slog.Logger
	handler := WithRequestLogger(base)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = httpx.Logger(r.Context(), nil)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got == nil {
		t.Fatal("expected logger in context, got nil")
	}

	got.Info("probe")

	if bytes.Contains(buf.Bytes(), []byte("request_id=")) {
		t.Errorf("expected no request_id attr when none is set, got: %s", buf.String())
	}
}

func TestWithRequestLogger_ExtraAttrs(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	called := 0
	extraFn := func(r *http.Request) slog.Attr {
		called++
		return slog.String("user_id", "u-999")
	}

	var got *slog.Logger
	handler := WithRequestLogger(base, extraFn)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = httpx.Logger(r.Context(), nil)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called != 1 {
		t.Errorf("expected extraFn to be called once, got %d", called)
	}

	if got == nil {
		t.Fatal("expected logger in context, got nil")
	}

	got.Info("probe")

	output := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("user_id=u-999")) {
		t.Errorf("expected user_id=u-999 in logger attrs, got: %s", output)
	}
}

func TestWithRequestLogger_MultipleExtraAttrs(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	fnA := func(r *http.Request) slog.Attr { return slog.String("tenant", "acme") }
	fnB := func(r *http.Request) slog.Attr { return slog.String("role", "admin") }

	var got *slog.Logger
	handler := WithRequestLogger(base, fnA, fnB)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = httpx.Logger(r.Context(), nil)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodDelete, "/api/resource/1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got == nil {
		t.Fatal("expected logger in context, got nil")
	}

	got.Info("probe")

	output := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("tenant=acme")) {
		t.Errorf("expected tenant=acme in logger attrs, got: %s", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte("role=admin")) {
		t.Errorf("expected role=admin in logger attrs, got: %s", output)
	}
}

func TestWithRequestLogger_InjectsLogger_WithCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var got *slog.Logger
	handler := WithRequestLogger(base)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = httpx.Logger(r.Context(), nil)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/trace", nil)
	ctx := httpx.SetCorrelationID(req.Context(), "corr-xyz-789")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got == nil {
		t.Fatal("expected logger in context, got nil")
	}

	got.Info("probe")

	output := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("correlation_id=corr-xyz-789")) {
		t.Errorf("expected correlation_id=corr-xyz-789 in logger attrs, got: %s", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte("method=GET")) {
		t.Errorf("expected method=GET in logger attrs, got: %s", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte("path=/api/trace")) {
		t.Errorf("expected path=/api/trace in logger attrs, got: %s", output)
	}
}

func TestWithRequestLogger_FallbackUnchanged(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fallback := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := WithRequestLogger(base)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := httpx.Logger(r.Context(), fallback)
		if logger == fallback {
			t.Error("expected injected logger, not fallback")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/check", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
}
