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

func TestClientIP_XForwardedFor_FromLoopbackProxy(t *testing.T) {
	// Default trusted-proxy list is loopback-only. With 127.0.0.1 as the
	// peer, X-Forwarded-For is honoured and the right-most-untrusted entry
	// is returned.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "203.0.113.50, 10.0.0.2")

	got := ClientIP(req)
	// 10.0.0.2 is NOT in the default trusted set anymore, so XFF walks
	// right-to-left and picks 10.0.0.2 (the right-most non-trusted entry).
	if got != "10.0.0.2" {
		t.Errorf("ClientIP = %q, want %q", got, "10.0.0.2")
	}
}

func TestClientIP_DefaultDoesNotTrustRFC1918(t *testing.T) {
	// Spoofing-protection test: a caller arriving from inside the VPC
	// (10.x.x.x) sends X-Forwarded-For. With the loopback-only default,
	// the header MUST be ignored and the RemoteAddr returned.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	req.Header.Set("X-Real-IP", "203.0.113.99")

	got := ClientIP(req)
	if got != "10.0.0.1" {
		t.Errorf("ClientIP = %q, want %q (RFC1918 peer must NOT be trusted by default)", got, "10.0.0.1")
	}
}

func TestClientIP_ExplicitTrustedProxies_HonoursXForwardedFor(t *testing.T) {
	// Operators behind a k8s ingress on 10.0.0.0/8 pass the ingress CIDR
	// explicitly. With that list, XFF is honoured.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "203.0.113.50, 10.0.0.2")

	trusted, err := ParseTrustedProxiesStrict([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("ParseTrustedProxiesStrict: %v", err)
	}
	got := ClientIPWithTrustedProxies(req, trusted)
	if got != "203.0.113.50" {
		t.Errorf("ClientIP = %q, want %q", got, "203.0.113.50")
	}
}

func TestClientIP_XForwardedFor_AllTrusted(t *testing.T) {
	// With 10.0.0.0/8 explicitly trusted, all XFF entries are trusted →
	// fallback to RemoteAddr.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "10.0.0.2, 10.0.0.3")

	trusted, err := ParseTrustedProxiesStrict([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("ParseTrustedProxiesStrict: %v", err)
	}
	got := ClientIPWithTrustedProxies(req, trusted)
	if got != "10.0.0.1" {
		t.Errorf("ClientIP = %q, want %q (all XFF IPs are trusted, fallback to RemoteAddr)", got, "10.0.0.1")
	}
}

func TestParseTrustedProxiesStrict_RejectsInvalid(t *testing.T) {
	_, err := ParseTrustedProxiesStrict([]string{"not-a-cidr"})
	if err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestParseTrustedProxiesStrict_AcceptsValid(t *testing.T) {
	got, err := ParseTrustedProxiesStrict([]string{"10.0.0.0/8", "192.168.1.1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 networks, got %d", len(got))
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
