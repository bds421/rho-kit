package secheaders

import (
	crypto_tls "crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(opts ...Option) *httptest.ResponseRecorder {
	return serveWithTLS(false, opts...)
}

func serveTLS(opts ...Option) *httptest.ResponseRecorder {
	return serveWithTLS(true, opts...)
}

func serveWithTLS(tls bool, opts ...Option) *httptest.ResponseRecorder {
	handler := New(opts...)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if tls {
		req.TLS = &crypto_tls.ConnectionState{}
	}
	handler.ServeHTTP(rec, req)
	return rec
}

func TestDefaults_PlainHTTP(t *testing.T) {
	rec := serve()

	tests := []struct {
		header string
		want   string
	}{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"Referrer-Policy", "strict-origin-when-cross-origin"},
		{"Permissions-Policy", "geolocation=(), microphone=(), camera=()"},
		{"Cache-Control", "no-store"},
		{"Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'"},
		{"Cross-Origin-Opener-Policy", "same-origin"},
		{"Cross-Origin-Embedder-Policy", "require-corp"},
		{"Cross-Origin-Resource-Policy", "same-origin"},
	}

	for _, tt := range tests {
		if got := rec.Header().Get(tt.header); got != tt.want {
			t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
		}
	}

	// HSTS must NOT be sent over plain HTTP (RFC 6797 §7.2).
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS sent over plain HTTP: %q", got)
	}
}

func TestDefaults_TLS(t *testing.T) {
	rec := serveTLS()

	// HSTS is only sent over TLS.
	if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=63072000; includeSubDomains" {
		t.Errorf("HSTS = %q, want default", got)
	}
}

func TestSameOrigin(t *testing.T) {
	rec := serve(WithFrameOption(SameOrigin))
	if got := rec.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}
}

func TestWithFrameOption_PanicsOnInvalid(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic on invalid X-Frame-Options value")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("panic = %T, want string", rec)
		}
		if strings.Contains(msg, "secret-token") {
			t.Fatalf("panic leaked invalid frame option: %q", msg)
		}
	}()
	WithFrameOption("ALLOWALL secret-token")
}

func TestDisableContentType(t *testing.T) {
	rec := serve(WithoutContentTypeNoSniff())
	if got := rec.Header().Get("X-Content-Type-Options"); got != "" {
		t.Errorf("X-Content-Type-Options = %q, want empty", got)
	}
}

func TestWithoutHSTS(t *testing.T) {
	// Even over TLS, WithoutHSTS should suppress the header.
	rec := serveTLS(WithoutHSTS())
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS = %q, want empty", got)
	}
}

func TestCustomCSP(t *testing.T) {
	rec := serve(WithContentSecurityPolicy("default-src 'self'"))
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'self'" {
		t.Errorf("CSP = %q, want custom", got)
	}
}

func TestCustomCacheControl(t *testing.T) {
	rec := serve(WithCacheControl("public, max-age=3600"))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want custom", got)
	}
}

func TestCustomReferrerPolicy(t *testing.T) {
	rec := serve(WithReferrerPolicy("no-referrer"))
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

func TestCustomPermissionsPolicy(t *testing.T) {
	rec := serve(WithPermissionsPolicy("camera=(self)"))
	if got := rec.Header().Get("Permissions-Policy"); got != "camera=(self)" {
		t.Errorf("Permissions-Policy = %q, want custom", got)
	}
}

func TestDisableAll(t *testing.T) {
	rec := serve(
		WithoutContentTypeNoSniff(),
		WithFrameOption(""),
		WithReferrerPolicy(""),
		WithPermissionsPolicy(""),
		WithoutHSTS(),
		WithCacheControl(""),
		WithContentSecurityPolicy(""),
		WithoutCrossOriginPolicies(),
	)

	for _, h := range []string{
		"X-Content-Type-Options",
		"X-Frame-Options",
		"Referrer-Policy",
		"Permissions-Policy",
		"Strict-Transport-Security",
		"Cache-Control",
		"Content-Security-Policy",
		"Cross-Origin-Opener-Policy",
		"Cross-Origin-Embedder-Policy",
		"Cross-Origin-Resource-Policy",
	} {
		if got := rec.Header().Get(h); got != "" {
			t.Errorf("%s = %q, want empty", h, got)
		}
	}
}

func TestCustomCOOP(t *testing.T) {
	rec := serve(WithCrossOriginOpenerPolicy("same-origin-allow-popups"))
	if got := rec.Header().Get("Cross-Origin-Opener-Policy"); got != "same-origin-allow-popups" {
		t.Errorf("COOP = %q, want same-origin-allow-popups", got)
	}
}

func TestCustomCOEP(t *testing.T) {
	rec := serve(WithCrossOriginEmbedderPolicy("credentialless"))
	if got := rec.Header().Get("Cross-Origin-Embedder-Policy"); got != "credentialless" {
		t.Errorf("COEP = %q, want credentialless", got)
	}
}

func TestCustomCORP(t *testing.T) {
	rec := serve(WithCrossOriginResourcePolicy("cross-origin"))
	if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Errorf("CORP = %q, want cross-origin", got)
	}
}

func TestWithoutCOOP(t *testing.T) {
	rec := serve(WithoutCrossOriginOpener())
	if got := rec.Header().Get("Cross-Origin-Opener-Policy"); got != "" {
		t.Errorf("COOP after WithoutCrossOriginOpener = %q, want empty", got)
	}
	// COEP/CORP still default.
	if got := rec.Header().Get("Cross-Origin-Embedder-Policy"); got != "require-corp" {
		t.Errorf("COEP = %q, want require-corp", got)
	}
	if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Errorf("CORP = %q, want same-origin", got)
	}
}

func TestWithoutCOEP(t *testing.T) {
	rec := serve(WithoutCrossOriginEmbedder())
	if got := rec.Header().Get("Cross-Origin-Embedder-Policy"); got != "" {
		t.Errorf("COEP after WithoutCrossOriginEmbedder = %q, want empty", got)
	}
	if got := rec.Header().Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
		t.Errorf("COOP = %q, want same-origin", got)
	}
}

func TestWithoutCORP(t *testing.T) {
	rec := serve(WithoutCrossOriginResource())
	if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "" {
		t.Errorf("CORP after WithoutCrossOriginResource = %q, want empty", got)
	}
}

func TestWithoutCrossOriginPolicies_DisablesAllThree(t *testing.T) {
	rec := serve(WithoutCrossOriginPolicies())
	for _, h := range []string{
		"Cross-Origin-Opener-Policy",
		"Cross-Origin-Embedder-Policy",
		"Cross-Origin-Resource-Policy",
	} {
		if got := rec.Header().Get(h); got != "" {
			t.Errorf("%s = %q, want empty", h, got)
		}
	}
}

func TestCrossOriginPolicyOptions_PanicOnInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) Option
	}{
		{"coop", WithCrossOriginOpenerPolicy},
		{"coep", WithCrossOriginEmbedderPolicy},
		{"corp", WithCrossOriginResourcePolicy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic on invalid header value")
				}
			}()
			tt.fn("ok\r\nX-Evil: 1")
		})
	}
}

func TestHSTS_BehindTrustedProxyXFP(t *testing.T) {
	// When TLS terminates before this service, r.TLS == nil but
	// X-Forwarded-Proto: https from a trusted proxy IP must enable HSTS.
	// Default behaviour silently dropped HSTS for common ingress topologies.
	_, ipnet, _ := net.ParseCIDR("10.0.0.0/8")
	handler := New(WithTrustedProxiesForProto([]*net.IPNet{ipnet}))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:9999"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("HSTS missing — trusted proxy with XFP=https must enable HSTS")
	}
}

func TestHSTS_UntrustedProxyXFPRejected(t *testing.T) {
	_, ipnet, _ := net.ParseCIDR("10.0.0.0/8")
	handler := New(WithTrustedProxiesForProto([]*net.IPNet{ipnet}))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Same XFP=https header but RemoteAddr is OUTSIDE the trusted CIDR —
	// must be ignored.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.1:9999"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS = %q on untrusted-proxy XFP, want empty", got)
	}
}

func TestHSTS_TrustedProxyDuplicateXFPUsesFirst(t *testing.T) {
	_, ipnet, _ := net.ParseCIDR("10.0.0.0/8")
	handler := New(WithTrustedProxiesForProto([]*net.IPNet{ipnet}))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:9999"
	// Multi-hop proxies append XFP values; accept the left-most (first)
	// hop under the operator's trusted-proxy declaration.
	req.Header.Add("X-Forwarded-Proto", "https")
	req.Header.Add("X-Forwarded-Proto", "http")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Fatalf("expected HSTS when first X-Forwarded-Proto is https (multi-value)")
	}
}

func TestHSTS_TrustedProxyInvalidXFPRejected(t *testing.T) {
	_, ipnet, _ := net.ParseCIDR("10.0.0.0/8")
	handler := New(WithTrustedProxiesForProto([]*net.IPNet{ipnet}))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, value := range []string{"https\n", "https\r", "https\x00", " \t "} {
		t.Run("invalid", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "10.0.0.5:9999"
			req.Header.Set("X-Forwarded-Proto", value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
				t.Errorf("HSTS = %q with invalid X-Forwarded-Proto, want empty", got)
			}
		})
	}
}

func TestWithTrustedProxiesForProto_PanicsOnNilEntry(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil trusted proxy CIDR")
		}
	}()
	WithTrustedProxiesForProto([]*net.IPNet{nil})
}

func TestWithTrustedProxiesForProto_ClonesCIDR(t *testing.T) {
	_, ipnet, _ := net.ParseCIDR("10.0.0.0/8")
	opt := WithTrustedProxiesForProto([]*net.IPNet{ipnet})
	ipnet.IP = net.ParseIP("203.0.113.0")
	ipnet.Mask = net.CIDRMask(24, 32)

	handler := New(opt)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:9999"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Fatal("trusted proxy option must not be affected by caller mutation after construction")
	}
}

func TestWithTrustedProxiesForProto_OptionReuseClonesOutput(t *testing.T) {
	_, ipnet, _ := net.ParseCIDR("10.0.0.0/8")
	opt := WithTrustedProxiesForProto([]*net.IPNet{ipnet})

	var cfg1 config
	opt(&cfg1)
	var cfg2 config
	opt(&cfg2)

	cfg1.trustedProxies[0].IP = net.ParseIP("203.0.113.0")
	if !cfg2.trustedProxies[0].Contains(net.ParseIP("10.0.0.5")) {
		t.Fatalf("second option application shared trusted proxy state: %v", cfg2.trustedProxies[0])
	}
}

func TestHSTS_ForceHSTS(t *testing.T) {
	handler := New(WithForceHSTS())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("HSTS missing despite WithForceHSTS")
	}
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	New(nil)
}

func TestHeaderValueOptions_PanicOnControlCharacters(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) Option
	}{
		{"referrer", WithReferrerPolicy},
		{"permissions", WithPermissionsPolicy},
		{"hsts", WithHSTS},
		{"cache", WithCacheControl},
		{"csp", WithContentSecurityPolicy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic on invalid header value")
				}
			}()
			tt.fn("ok\r\nX-Evil: 1")
		})
	}
}

func TestHeaderValueOptions_PanicOnOuterWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		fn    func(string) Option
		value string
	}{
		{"referrer", WithReferrerPolicy, " no-referrer"},
		{"permissions", WithPermissionsPolicy, "camera=() "},
		{"hsts", WithHSTS, "\tmax-age=60"},
		{"cache", WithCacheControl, " \t "},
		{"csp", WithContentSecurityPolicy, " default-src 'self'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic on whitespace-padded header value")
				}
			}()
			tt.fn(tt.value)
		})
	}
}

// TestHeaderValueOptions_PanicMessageNamesHeaderWithoutLeakingValue verifies
// that a misconfigured value-bearing option panics with a message that
// identifies which header failed (so an operator can locate the offending
// option among several in one New(...) call) while still withholding the
// invalid value itself.
func TestHeaderValueOptions_PanicMessageNamesHeaderWithoutLeakingValue(t *testing.T) {
	const secret = "secret-token-value"
	tests := []struct {
		name   string
		fn     func(string) Option
		header string
		value  string
	}{
		{"referrer-control", WithReferrerPolicy, "Referrer-Policy", "ok\r\n" + secret},
		{"permissions-control", WithPermissionsPolicy, "Permissions-Policy", "ok\r\n" + secret},
		{"hsts-control", WithHSTS, "Strict-Transport-Security", "ok\r\n" + secret},
		{"cache-control", WithCacheControl, "Cache-Control", "ok\r\n" + secret},
		{"csp-control", WithContentSecurityPolicy, "Content-Security-Policy", "ok\r\n" + secret},
		{"referrer-whitespace", WithReferrerPolicy, "Referrer-Policy", " " + secret},
		{"csp-whitespace", WithContentSecurityPolicy, "Content-Security-Policy", secret + " "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				rec := recover()
				if rec == nil {
					t.Fatal("expected panic on invalid header value")
				}
				msg, ok := rec.(string)
				if !ok {
					t.Fatalf("panic = %T, want string", rec)
				}
				if !strings.Contains(msg, tt.header) {
					t.Fatalf("panic message %q does not name the %s header", msg, tt.header)
				}
				if strings.Contains(msg, secret) {
					t.Fatalf("panic leaked invalid header value: %q", msg)
				}
			}()
			tt.fn(tt.value)
		})
	}
}
