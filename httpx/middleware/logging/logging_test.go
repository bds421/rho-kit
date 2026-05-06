package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogger_WithClientIPResolverHonoursCustomResolver(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := LoggerWithOptions(logger, nil,
		[]LoggerOption{WithClientIPResolver(func(_ *http.Request) string { return "203.0.113.99" })},
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "127.0.0.1:54321" // resolver should ignore this
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !bytes.Contains(buf.Bytes(), []byte("remote=203.0.113.99")) {
		t.Errorf("custom resolver value not in log line: %s", buf.String())
	}
}

func TestLogger_NormalPath(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := Logger(logger, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	logOutput := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("level=INFO")) {
		t.Errorf("expected INFO level log, got: %s", logOutput)
	}
	if !bytes.Contains(buf.Bytes(), []byte("path=/api/test")) {
		t.Errorf("expected path in log, got: %s", logOutput)
	}
}

func TestLogger_QuietPath(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := Logger(logger, []string{"/health"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	logOutput := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("level=DEBUG")) {
		t.Errorf("expected DEBUG level for quiet path, got: %s", logOutput)
	}
}

func TestLogger_ExtraAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := Logger(logger, nil, func(r *http.Request) slog.Attr {
		return slog.String("custom", "value")
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	logOutput := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("custom=value")) {
		t.Errorf("expected custom attr in log, got: %s", logOutput)
	}
}

func TestLogger_OmitsEmptyKeyAttr(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := Logger(logger, nil,
		func(r *http.Request) slog.Attr {
			return slog.Attr{} // zero-value attr with empty key
		},
		func(r *http.Request) slog.Attr {
			return slog.String("keep", "this")
		},
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	logOutput := buf.String()
	// TextHandler renders a zero-value slog.Attr as ="" — check it was filtered out.
	if bytes.Contains(buf.Bytes(), []byte("=\"\"")) {
		t.Errorf("log should not contain empty-key attr, got: %s", logOutput)
	}
	if !bytes.Contains(buf.Bytes(), []byte("keep=this")) {
		t.Errorf("expected keep=this in log, got: %s", logOutput)
	}
}

func TestLogger_CapturesStatus(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := Logger(logger, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !bytes.Contains(buf.Bytes(), []byte("status=404")) {
		t.Errorf("expected status=404 in log, got: %s", buf.String())
	}
}
