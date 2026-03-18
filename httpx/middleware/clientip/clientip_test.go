package clientip

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP_RemoteAddrOnly(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.50:12345"

	got := ClientIP(req)
	if got != "203.0.113.50" {
		t.Errorf("ClientIP = %q, want %q", got, "203.0.113.50")
	}
}

func TestClientIP_XRealIP_FromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:8080"
	req.Header.Set("X-Real-IP", "203.0.113.50")

	got := ClientIP(req)
	if got != "203.0.113.50" {
		t.Errorf("ClientIP = %q, want %q", got, "203.0.113.50")
	}
}

func TestClientIP_XRealIP_FromUntrustedAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.1:12345"
	req.Header.Set("X-Real-IP", "1.2.3.4")

	got := ClientIP(req)
	if got != "203.0.113.1" {
		t.Errorf("ClientIP = %q, want %q (should ignore X-Real-IP from untrusted addr)", got, "203.0.113.1")
	}
}

func TestClientIP_XForwardedFor_FromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "203.0.113.50, 10.0.0.2")

	got := ClientIP(req)
	if got != "203.0.113.50" {
		t.Errorf("ClientIP = %q, want %q", got, "203.0.113.50")
	}
}

func TestClientIP_XForwardedFor_AllTrusted(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "10.0.0.2, 10.0.0.3")

	got := ClientIP(req)
	if got != "10.0.0.1" {
		t.Errorf("ClientIP = %q, want %q (all XFF IPs are trusted, fallback to RemoteAddr)", got, "10.0.0.1")
	}
}

func TestClientIP_NoPort(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.50"

	got := ClientIP(req)
	if got != "203.0.113.50" {
		t.Errorf("ClientIP = %q, want %q", got, "203.0.113.50")
	}
}

func TestClientIP_IPv6(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "[::1]:8080"
	req.Header.Set("X-Forwarded-For", "2001:db8::1")

	got := ClientIP(req)
	if got != "2001:db8::1" {
		t.Errorf("ClientIP = %q, want %q", got, "2001:db8::1")
	}
}
