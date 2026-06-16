package logging

import (
	"bufio"
	"bytes"
	"errors"
	"log/slog"
	"net"
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

func TestLoggerWithOptions_ClonesExtraAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	extra := []func(*http.Request) slog.Attr{
		func(*http.Request) slog.Attr { return slog.String("extra", "original") },
	}
	handler := LoggerWithOptions(logger, nil, nil, extra...)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	extra[0] = func(*http.Request) slog.Attr { return slog.String("extra", "mutated") }

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !bytes.Contains(buf.Bytes(), []byte("extra=original")) {
		t.Fatalf("expected original extra attr, got: %s", buf.String())
	}
	if bytes.Contains(buf.Bytes(), []byte("extra=mutated")) {
		t.Fatalf("unexpected mutated extra attr, got: %s", buf.String())
	}
}

func TestLoggerWithOptions_ClonesTrustedProxies(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	_, trusted, err := net.ParseCIDR("192.0.2.0/24")
	if err != nil {
		t.Fatal(err)
	}
	opt := WithTrustedProxies([]*net.IPNet{trusted})
	trusted.IP = net.ParseIP("10.0.0.0")

	handler := LoggerWithOptions(logger, nil, []LoggerOption{opt})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "192.0.2.10:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !bytes.Contains(buf.Bytes(), []byte("remote=203.0.113.10")) {
		t.Fatalf("expected original trusted proxy CIDR to be used, got: %s", buf.String())
	}
}

func assertLogPathRedacted(t *testing.T, logOutput []byte, rawPath string) {
	t.Helper()
	if !bytes.Contains(logOutput, []byte("path=\"<redacted")) {
		t.Fatalf("expected redacted path attr, got: %s", string(logOutput))
	}
	if rawPath != "" && bytes.Contains(logOutput, []byte(rawPath)) {
		t.Fatalf("raw path %q leaked into log: %s", rawPath, string(logOutput))
	}
}

func TestLoggerWithOptions_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	LoggerWithOptions(slog.Default(), nil, []LoggerOption{nil})
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
	assertLogPathRedacted(t, buf.Bytes(), "/api/test")
}

func TestLogger_UsesEscapedPath(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := Logger(logger, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/files/a%2Fb", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	logOutput := buf.String()
	assertLogPathRedacted(t, buf.Bytes(), "/api/files/a%2Fb")
	if bytes.Contains(buf.Bytes(), []byte("path=/api/files/a/b")) {
		t.Errorf("decoded path delimiter should not be logged, got: %s", logOutput)
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

func TestLogger_ExtraAttrPanicDoesNotPanic(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := Logger(logger, nil,
		func(*http.Request) slog.Attr {
			panic("attr exploded")
		},
		func(*http.Request) slog.Attr {
			return slog.String("keep", "this")
		},
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	logOutput := buf.String()
	if bytes.Contains(buf.Bytes(), []byte("attr=exploded")) {
		t.Errorf("panicking attr should be omitted, got: %s", logOutput)
	}
	if !bytes.Contains(buf.Bytes(), []byte("keep=this")) {
		t.Errorf("later attrs should still be logged, got: %s", logOutput)
	}
}

func TestLogger_ClientIPResolverPanicDoesNotPanic(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := LoggerWithOptions(logger, nil,
		[]LoggerOption{WithClientIPResolver(func(*http.Request) string {
			panic("resolver exploded")
		})},
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !bytes.Contains(buf.Bytes(), []byte("remote=\"\"")) {
		t.Errorf("expected empty remote after resolver panic, got: %s", buf.String())
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

func TestLogger_NilLoggerNormalized(t *testing.T) {
	// Constructing with nil must not panic, and nil must be normalized so
	// each request can be served without panicking either.
	handler := Logger(nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestLoggerWithOptions_NilLoggerNormalized(t *testing.T) {
	handler := LoggerWithOptions(nil, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
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

func TestLogger_Logs500AndRepanicsWhenHandlerPanicsBeforeHeaders(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handlerPanic := errors.New("handler panic")

	handler := Logger(logger, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(handlerPanic)
	}))

	defer func() {
		got := recover()
		if got != handlerPanic {
			t.Fatalf("panic = %v, want original panic", got)
		}
		logOutput := buf.String()
		if !bytes.Contains(buf.Bytes(), []byte("status=500")) {
			t.Fatalf("expected status=500 in panic access log, got: %s", logOutput)
		}
		if !bytes.Contains(buf.Bytes(), []byte("panicked=true")) {
			t.Fatalf("expected panicked=true in access log, got: %s", logOutput)
		}
	}()

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))
}

func TestWithClientIPResolver_PanicsOnNilResolver(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil resolver")
		}
	}()
	WithClientIPResolver(nil)
}

// hijackableRecorder is an httptest.ResponseRecorder that also implements
// http.Hijacker, returning a nil error so the kit's ResponseRecorder latches
// WasHijacked() (mirroring a successful WebSocket upgrade).
type hijackableRecorder struct {
	*httptest.ResponseRecorder
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	// Return a non-nil net.Conn so the recorder treats the hijack as
	// successful; the conn is never used by the test handler.
	c1, _ := net.Pipe()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

func TestLogger_HijackedRequestLogs101AndHijackedAttr(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := Logger(logger, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatalf("wrapped writer did not expose http.Hijacker")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack failed: %v", err)
		}
		_ = conn.Close()
		// Deliberately do NOT call WriteHeader: a hijacked WebSocket upgrade
		// writes 101 to the raw conn, so the recorder's status stays at its
		// default 200 unless the middleware special-cases hijacking.
	}))

	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	handler.ServeHTTP(rec, req)

	logOutput := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("status=101")) {
		t.Fatalf("expected status=101 for hijacked request, got: %s", logOutput)
	}
	if !bytes.Contains(buf.Bytes(), []byte("hijacked=true")) {
		t.Fatalf("expected hijacked=true attr, got: %s", logOutput)
	}
	if bytes.Contains(buf.Bytes(), []byte("status=200")) {
		t.Fatalf("hijacked request must not be logged with bogus status=200, got: %s", logOutput)
	}
}

func TestLogger_NonHijackedRequestOmitsHijackedAttr(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := Logger(logger, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if bytes.Contains(buf.Bytes(), []byte("hijacked=true")) {
		t.Fatalf("non-hijacked request must not carry hijacked attr, got: %s", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("status=200")) {
		t.Fatalf("expected status=200 for normal request, got: %s", buf.String())
	}
}

func TestLogger_PanicAfterHeaderLogsWrittenStatusAndRepanics(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handlerPanic := errors.New("handler panic")

	handler := Logger(logger, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic(handlerPanic)
	}))

	defer func() {
		got := recover()
		if got != handlerPanic {
			t.Fatalf("panic = %v, want original panic", got)
		}
		logOutput := buf.String()
		if !bytes.Contains(buf.Bytes(), []byte("status=202")) {
			t.Fatalf("expected written status in panic access log, got: %s", logOutput)
		}
		if !bytes.Contains(buf.Bytes(), []byte("panicked=true")) {
			t.Fatalf("expected panicked=true in access log, got: %s", logOutput)
		}
	}()

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/accepted", nil))
}
