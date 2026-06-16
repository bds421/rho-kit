package auditlog

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

// recordingStore captures emitted events so tests can assert on them.
type recordingStore struct {
	events []auditlog.Event
}

func (s *recordingStore) Append(_ context.Context, e auditlog.Event) error {
	s.events = append(s.events, e)
	return nil
}

func (s *recordingStore) AppendChained(_ context.Context, build func(prev []byte) (auditlog.Event, error)) error {
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

func (s *recordingStore) Query(_ context.Context, _ auditlog.Filter, _ string, _ int) ([]auditlog.Event, string, error) {
	return nil, "", nil
}

func (s *recordingStore) RangeChain(_ context.Context, fn func(auditlog.Event) error) error {
	for _, e := range s.events {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func (s *recordingStore) LastHMAC(_ context.Context) ([]byte, error) {
	if len(s.events) == 0 {
		return nil, nil
	}
	return s.events[len(s.events)-1].HMAC, nil
}

// testAuditKey is a deterministic 32-byte key for unit tests; production
// services source chain/cursor keys from KMS or config secrets.
var testAuditKey = bytes.Repeat([]byte{0xab}, 32)

func newLogger() (*recordingStore, *auditlog.Logger) {
	store := &recordingStore{}
	return store, auditlog.New(store,
		auditlog.WithChainKey(testAuditKey),
		auditlog.WithCursorKey(testAuditKey),
	)
}

func TestAuditlog_DefaultClientIPNoTrustedProxies(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "203.0.113.4:12345" // public IP, not a trusted proxy
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if len(store.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(store.events))
	}
	got := store.events[0].IPAddress
	// With no trusted proxies, X-Forwarded-For is ignored.
	if got != "203.0.113.4" {
		t.Errorf("IPAddress = %q, want %q (XFF should be ignored when no trusted proxies)", got, "203.0.113.4")
	}
}

type hijackableResponseWriter struct {
	*httptest.ResponseRecorder
}

func (h *hijackableResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, nil
}

func TestAuditlog_PreservesHijackerInterface(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Hijacker); !ok {
			t.Fatal("expected audit middleware ResponseWriter to preserve http.Hijacker")
		}
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	rec := &hijackableResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(rec, req)

	if len(store.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(store.events))
	}
	if got := rec.Code; got != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want %d", got, http.StatusSwitchingProtocols)
	}
}

func TestAuditlog_HonorsTrustedProxyXFF(t *testing.T) {
	store, l := newLogger()
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	mw := Middleware(l, WithTrustedProxies([]*net.IPNet{loopback}))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "127.0.0.1:54321" // trusted proxy
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if len(store.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(store.events))
	}
	if got := store.events[0].IPAddress; got != "203.0.113.5" {
		t.Errorf("IPAddress = %q, want %q (XFF should be honored from trusted proxy)", got, "203.0.113.5")
	}
}

func TestAuditlog_TrustedProxiesAreDetached(t *testing.T) {
	store, l := newLogger()
	_, trusted, err := net.ParseCIDR("192.0.2.0/24")
	if err != nil {
		t.Fatal(err)
	}
	opt := WithTrustedProxies([]*net.IPNet{trusted})
	trusted.IP = net.ParseIP("10.0.0.0")

	mw := Middleware(l, opt)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "192.0.2.10:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if len(store.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(store.events))
	}
	if got := store.events[0].IPAddress; got != "203.0.113.10" {
		t.Fatalf("IPAddress = %q, want %q", got, "203.0.113.10")
	}
}

func TestAuditlog_PreservesRequestTraceID(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatalf("trace id: %v", err)
	}
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatalf("span id: %v", err)
	}
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req = req.WithContext(trace.ContextWithSpanContext(req.Context(), spanCtx))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if len(store.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(store.events))
	}
	if got := store.events[0].TraceID; got != traceID.String() {
		t.Fatalf("TraceID = %q, want %q", got, traceID.String())
	}
}

func TestAuditlog_PanicsOnNilLogger(t *testing.T) {
	defer func() {
		if rcv := recover(); rcv == nil {
			t.Fatal("expected panic when Middleware called with nil logger")
		}
	}()
	_ = Middleware(nil)
}

func TestAuditlog_PanicsOnNilOptions(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{name: "actor", fn: func() { WithActorExtractor(nil) }},
		{name: "path", fn: func() { WithPathFilter(nil) }},
		{name: "status", fn: func() { WithStatusFilter(nil) }},
		{name: "client ip", fn: func() { WithClientIPFunc(nil) }},
		{name: "middleware option", fn: func() {
			_, l := newLogger()
			Middleware(l, nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic")
				}
			}()
			tt.fn()
		})
	}
}

func TestAuditlog_RecordsOnPanic(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l)
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic(errors.New("boom"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	defer func() {
		// We expect the panic to be re-raised so an upstream recover
		// middleware can write the 500. Recover here to avoid failing
		// the test, then assert the audit entry was written.
		_ = recover()
		if len(store.events) != 1 {
			t.Fatalf("expected audit entry written before panic re-raise, got %d events", len(store.events))
		}
		ev := store.events[0]
		if got := ev.Status; got != "failure" {
			t.Errorf("Status = %q, want %q on panic", got, "failure")
		}
		if string(ev.Metadata) != string(panicMetadataJSON) {
			t.Errorf("panic Metadata = %s, want %s", ev.Metadata, panicMetadataJSON)
		}
	}()
	h.ServeHTTP(rec, req)
}

func TestAuditlog_PathFilterPanicAuditsAndContinues(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l, WithPathFilter(func(string) bool {
		panic("filter failed")
	}))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected audit event after path filter panic, got %d", len(store.events))
	}
}

func TestAuditlog_StatusFilterPanicAudits(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l, WithStatusFilter(func(int) bool {
		panic("status filter failed")
	}))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if len(store.events) != 1 {
		t.Fatalf("expected audit event after status filter panic, got %d", len(store.events))
	}
}

func TestAuditlog_ExtractorPanicsUseFallbacks(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l,
		WithClientIPFunc(func(*http.Request) string {
			panic("ip failed")
		}),
		WithActorExtractor(func(*http.Request) string {
			panic("actor failed")
		}),
	)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if len(store.events) != 1 {
		t.Fatalf("expected audit event after extractor panics, got %d", len(store.events))
	}
	ev := store.events[0]
	if ev.IPAddress != "" {
		t.Errorf("IPAddress = %q, want empty fallback", ev.IPAddress)
	}
	if ev.Actor != "anonymous" {
		t.Errorf("Actor = %q, want anonymous fallback", ev.Actor)
	}
}

func TestAuditlog_UsesEscapedPathForResource(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/a%20b", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if len(store.events) != 1 {
		t.Fatalf("expected audit event for escaped path, got %d", len(store.events))
	}
	if got := store.events[0].Resource; got != "/api/a%20b" {
		t.Fatalf("Resource = %q, want escaped path", got)
	}
}

func TestAuditlog_PathFilterUsesEscapedPath(t *testing.T) {
	store, l := newLogger()
	var sawPath string
	mw := Middleware(l, WithPathFilter(func(path string) bool {
		sawPath = path
		return path != "/api/a%2Fb"
	}))
	handlerRan := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerRan = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/a%2Fb", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if !handlerRan {
		t.Fatal("handler should run when audit path filter skips")
	}
	if sawPath != "/api/a%2Fb" {
		t.Fatalf("path filter saw %q, want escaped path", sawPath)
	}
	if len(store.events) != 0 {
		t.Fatalf("expected path filter to skip audit event, got %d", len(store.events))
	}
}

func TestAuditlog_DefaultPathFilterDoesNotSkipPrefixCollisions(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/health-delete-user", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if len(store.events) != 1 {
		t.Fatalf("expected prefix-collision path to be audited, got %d events", len(store.events))
	}
	if got := store.events[0].Resource; got != "/health-delete-user" {
		t.Fatalf("Resource = %q, want audited request path", got)
	}
}

func TestAuditlog_DefaultPathFilterSkipsOpsSubpaths(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/health", "/health/live", "/ready", "/ready/db", "/metrics", "/metrics/prometheus"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}

	if len(store.events) != 0 {
		t.Fatalf("expected default ops paths to be skipped, got %d events", len(store.events))
	}
}

func TestAuditlog_InvalidExtractorValuesDoNotDropEvent(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l,
		WithActorExtractor(func(*http.Request) string {
			return "alice smith"
		}),
		WithClientIPFunc(func(*http.Request) string {
			return "203.0.113.10\n"
		}),
	)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if len(store.events) != 1 {
		t.Fatalf("expected invalid extractor values to fallback, got %d events", len(store.events))
	}
	ev := store.events[0]
	if ev.Actor != "anonymous" {
		t.Fatalf("Actor = %q, want anonymous fallback", ev.Actor)
	}
	if ev.IPAddress != "" {
		t.Fatalf("IPAddress = %q, want empty fallback", ev.IPAddress)
	}
}

func TestAuditlog_LongPathUsesSafeFallbackResource(t *testing.T) {
	store, l := newLogger()
	mw := Middleware(l)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/"+strings.Repeat("a", auditlog.MaxResourceBytes), nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if len(store.events) != 1 {
		t.Fatalf("expected long path to fallback, got %d events", len(store.events))
	}
	if got := store.events[0].Resource; !strings.HasPrefix(got, "path-invalid-sha256-") {
		t.Fatalf("Resource = %q, want hashed fallback", got)
	}
}

func TestSafeAuditIPAddress(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "ipv4", in: "203.0.113.10", want: "203.0.113.10"},
		{name: "ipv6", in: "fe80::1", want: "fe80::1"},
		{name: "ipv6 with zone", in: "fe80::1%eth0", want: "fe80::1%eth0"},
		{name: "ipv4 trailing newline", in: "203.0.113.10\n", want: ""},
		{name: "not an address", in: "not-an-ip", want: ""},
		// netip.ParseAddr does not validate the IPv6 zone, so control
		// characters and spaces smuggled after '%' must be rejected here to
		// prevent audit-log injection via WithClientIPFunc resolvers.
		{name: "zone newline", in: "fe80::1%a\nb", want: ""},
		{name: "zone tab", in: "fe80::1%a\tb", want: ""},
		{name: "zone null", in: "fe80::1%a\x00b", want: ""},
		{name: "zone space", in: "fe80::1%a b", want: ""},
		{name: "zone carriage return", in: "fe80::1%a\rb", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeAuditIPAddress(tt.in); got != tt.want {
				t.Fatalf("safeAuditIPAddress(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
