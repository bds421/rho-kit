package clientip

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestClientIP_NilRequestReturnsEmpty(t *testing.T) {
	if got := ClientIP(nil); got != "" {
		t.Errorf("ClientIP(nil) = %q, want empty", got)
	}
}

func TestClientIP_InvalidRemoteAddrReturnsEmpty(t *testing.T) {
	req := &http.Request{RemoteAddr: "not an ip", Header: make(http.Header)}

	if got := ClientIP(req); got != "" {
		t.Errorf("ClientIP invalid remote = %q, want empty", got)
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

func TestClientIP_XRealIP_DuplicateDoesNotBlockXForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:8080"
	req.Header.Add("X-Real-IP", "203.0.113.50")
	req.Header.Add("X-Real-IP", "198.51.100.10")
	req.Header.Set("X-Forwarded-For", "203.0.113.99")

	got := ClientIP(req)
	if got != "203.0.113.99" {
		t.Errorf("ClientIP = %q, want X-Forwarded-For when X-Real-IP is duplicated", got)
	}
}

func TestClientIP_XRealIP_InvalidDoesNotBlockXForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:8080"
	req.Header.Set("X-Real-IP", "not-an-ip")
	req.Header.Set("X-Forwarded-For", "203.0.113.99")

	got := ClientIP(req)
	if got != "203.0.113.99" {
		t.Errorf("ClientIP = %q, want X-Forwarded-For when X-Real-IP is invalid", got)
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

func TestClientIP_XForwardedFor_MultipleHeaderLinesAreOneChain(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:8080"
	req.Header.Add("X-Forwarded-For", "203.0.113.50")
	req.Header.Add("X-Forwarded-For", "10.0.0.2")

	got := ClientIP(req)
	if got != "10.0.0.2" {
		t.Errorf("ClientIP = %q, want right-most untrusted IP across all XFF values", got)
	}
}

func TestClientIP_TrustedProxyNilEntryFailsClosed(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")

	got := ClientIPWithTrustedProxies(req, []*net.IPNet{nil})
	if got != "10.0.0.1" {
		t.Errorf("ClientIP = %q, want RemoteAddr when trusted proxy entry is nil", got)
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
	_, err := ParseTrustedProxiesStrict([]string{"not-a-cidr-secret-token"})
	if err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "not-a-cidr") {
		t.Fatalf("error leaked trusted proxy entry: %v", err)
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

func TestParseTrustedProxies_RejectsInvalid(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for invalid trusted proxy")
		}
	}()
	_ = ParseTrustedProxies([]string{"not-a-cidr"})
}

func TestParseTrustedProxies_EmptyUsesDefault(t *testing.T) {
	got := ParseTrustedProxies(nil)
	if len(got) == 0 {
		t.Fatal("expected default trusted proxy ranges")
	}
	if !isTrustedAddr("127.0.0.1:8080", got) {
		t.Fatal("expected loopback address to be trusted by default")
	}
}

func TestParseTrustedProxies_EmptyReturnsDefensiveCopy(t *testing.T) {
	got := ParseTrustedProxies(nil)
	got[0].IP = net.ParseIP("0.0.0.0")
	got[0].Mask = net.CIDRMask(0, 32)

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("X-Real-IP", "198.51.100.99")

	if client := ClientIP(req); client != "203.0.113.10" {
		t.Fatalf("mutating returned default proxies changed package defaults: got %q", client)
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

// TestClientIP_XForwardedFor_PortAppended covers proxies (Azure Application
// Gateway, IIS/ARR) that append "ip:port" forms to X-Forwarded-For. The
// host portion must be parsed and the right-most-untrusted entry returned,
// rather than skipping the entry as garbage and walking left into an
// attacker-prepended value.
func TestClientIP_XForwardedFor_PortAppended(t *testing.T) {
	tests := []struct {
		name string
		xff  string
		want string
	}{
		{
			name: "ipv4 with port is parsed not skipped",
			xff:  "203.0.113.5:35123",
			want: "203.0.113.5",
		},
		{
			name: "attacker-prepended IP does not win over port-appended real client",
			// Client supplies "6.6.6.6"; proxy appends the real peer with a port.
			// The right-most entry is the real client and must win.
			xff:  "6.6.6.6, 203.0.113.5:35123",
			want: "203.0.113.5",
		},
		{
			name: "ipv6 with port is parsed not skipped",
			xff:  "[2001:db8::1]:443",
			want: "2001:db8::1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "127.0.0.1:8080"
			req.Header.Set("X-Forwarded-For", tt.xff)

			got := ClientIP(req)
			if got != tt.want {
				t.Errorf("ClientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClientIP_XForwardedFor_UnparseableFailsClosed covers entries that are
// neither a bare IP nor a valid host:port. Walking left past such an entry
// can return an earlier, fully attacker-supplied value. The handler must
// fail closed to RemoteAddr instead.
func TestClientIP_XForwardedFor_UnparseableFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		xff  string
		want string
	}{
		{
			name: "hostname garbage right of attacker IP fails closed to RemoteAddr",
			// "garbage" is unparseable; failing closed must NOT return the
			// attacker-prepended 6.6.6.6.
			xff:  "6.6.6.6, garbage",
			want: "127.0.0.1",
		},
		{
			name: "single unparseable entry fails closed to RemoteAddr",
			xff:  "not-an-ip",
			want: "127.0.0.1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "127.0.0.1:8080"
			req.Header.Set("X-Forwarded-For", tt.xff)

			got := ClientIP(req)
			if got != tt.want {
				t.Errorf("ClientIP = %q, want %q", got, tt.want)
			}
		})
	}
}
