package auditlog

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bds421/rho-kit/observability/auditlog"
)

// recordingStore captures emitted events so tests can assert on them.
type recordingStore struct {
	events []auditlog.Event
}

func (s *recordingStore) Append(_ context.Context, e auditlog.Event) error {
	s.events = append(s.events, e)
	return nil
}

func (s *recordingStore) Query(_ context.Context, _ auditlog.Filter, _ string, _ int) ([]auditlog.Event, string, error) {
	return nil, "", nil
}

func newLogger() (*recordingStore, *auditlog.Logger) {
	store := &recordingStore{}
	return store, auditlog.New(store)
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
