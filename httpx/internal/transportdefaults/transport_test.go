package transportdefaults

import (
	"crypto/tls"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNew_AppliesKitDefaults(t *testing.T) {
	tr := New(nil, 0, "test")
	if tr.MaxIdleConnsPerHost != DefaultMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d", tr.MaxIdleConnsPerHost, DefaultMaxIdleConnsPerHost)
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("expected TLSClientConfig to be set after New")
	}
	if tr.TLSClientConfig.MinVersion < MinimumTLSVersion {
		t.Errorf("MinVersion = %d, want >= %d (TLS 1.2)", tr.TLSClientConfig.MinVersion, MinimumTLSVersion)
	}
}

func TestNew_HonoursIdleConnTimeout(t *testing.T) {
	tr := New(nil, 45*time.Second, "test")
	if tr.IdleConnTimeout != 45*time.Second {
		t.Errorf("IdleConnTimeout = %s, want 45s", tr.IdleConnTimeout)
	}
}

func TestNew_ZeroIdleConnTimeoutPreservesClonedDefault(t *testing.T) {
	tr := New(nil, 0, "test")
	// Whatever the stdlib default is, it must not be silently zeroed.
	if tr.IdleConnTimeout == 0 {
		t.Error("IdleConnTimeout zeroed when caller passed 0; expected the cloned default")
	}
}

func TestNew_ClonesCallerTLSConfig(t *testing.T) {
	caller := &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
	tr := New(caller, 0, "test")
	if tr.TLSClientConfig == caller {
		t.Error("New must not retain the caller's *tls.Config; expected a clone")
	}
	// The NextProtos slice must propagate into the clone.
	if len(tr.TLSClientConfig.NextProtos) != 2 || tr.TLSClientConfig.NextProtos[0] != "h2" {
		t.Errorf("NextProtos = %v, want [h2 http/1.1]", tr.TLSClientConfig.NextProtos)
	}
}

func TestCloneTLSConfigWithFloor_RaisesMinVersionFloor(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS10}
	cloned := CloneTLSConfigWithFloor(cfg, "test")
	if cloned == cfg {
		t.Error("CloneTLSConfigWithFloor must return a clone, not the caller's pointer")
	}
	if cloned.MinVersion < MinimumTLSVersion {
		t.Errorf("MinVersion = %d, want >= TLS 1.2", cloned.MinVersion)
	}
}

func TestCloneTLSConfigWithFloor_HonoursHigherCallerFloor(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	cloned := CloneTLSConfigWithFloor(cfg, "test")
	if cloned.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %d, want TLS 1.3 (caller's higher floor preserved)", cloned.MinVersion)
	}
}

func TestCloneTLSConfigWithFloor_NilCfgReturnsFreshFloor(t *testing.T) {
	cloned := CloneTLSConfigWithFloor(nil, "test")
	if cloned == nil {
		t.Fatal("expected a non-nil *tls.Config")
	}
	if cloned.MinVersion < MinimumTLSVersion {
		t.Errorf("MinVersion = %d, want >= TLS 1.2", cloned.MinVersion)
	}
}

func TestCloneTLSConfigWithFloor_PanicsOnInsecureSkipVerify(t *testing.T) {
	cfg := &tls.Config{InsecureSkipVerify: true}
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic on InsecureSkipVerify=true")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("panic must be a string, got %T", rec)
		}
		if !strings.Contains(msg, "InsecureSkipVerify=true is not permitted") {
			t.Errorf("panic message = %q, want it to flag InsecureSkipVerify", msg)
		}
	}()
	_ = CloneTLSConfigWithFloor(cfg, "test")
}

func TestCloneTLSConfigWithFloor_PanicsOnLowMaxVersion(t *testing.T) {
	cfg := &tls.Config{MaxVersion: tls.VersionTLS11}
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic on MaxVersion below TLS 1.2")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("panic must be a string, got %T", rec)
		}
		if !strings.Contains(msg, "TLS MaxVersion") {
			t.Errorf("panic message = %q, want it to mention TLS MaxVersion", msg)
		}
	}()
	_ = CloneTLSConfigWithFloor(cfg, "test")
}

func TestFallback_ReturnsStdlibShapedTransport(t *testing.T) {
	tr := Fallback()
	if tr == nil {
		t.Fatal("Fallback returned nil")
	}
	if tr.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %d, want 100", tr.MaxIdleConns)
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %s, want 90s", tr.IdleConnTimeout)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %s, want 10s", tr.TLSHandshakeTimeout)
	}
	if tr.ExpectContinueTimeout != 1*time.Second {
		t.Errorf("ExpectContinueTimeout = %s, want 1s", tr.ExpectContinueTimeout)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 = false, want true (HTTP/2 must be attempted by default)")
	}
	if tr.DialContext == nil {
		t.Error("DialContext is nil; Fallback must wire a Dialer.DialContext")
	}
}

func TestNew_UsesFallbackWhenDefaultTransportReplaced(t *testing.T) {
	// Swap http.DefaultTransport for an arbitrary RoundTripper so the
	// type-assertion in New misses and the Fallback path is taken.
	type rt struct{}
	prev := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(_ *http.Request) (*http.Response, error) { return nil, nil })
	t.Cleanup(func() { http.DefaultTransport = prev })

	tr := New(nil, 0, "test")
	if tr == nil {
		t.Fatal("expected New to fall back to a fresh *http.Transport")
	}
	if tr.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %d, want 100 from Fallback", tr.MaxIdleConns)
	}
	_ = rt{}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
