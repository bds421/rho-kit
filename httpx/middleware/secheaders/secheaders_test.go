package secheaders

import (
	crypto_tls "crypto/tls"
	"net/http"
	"net/http/httptest"
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
		{"Content-Security-Policy", "default-src 'none'"},
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
	)

	for _, h := range []string{
		"X-Content-Type-Options",
		"X-Frame-Options",
		"Referrer-Policy",
		"Permissions-Policy",
		"Strict-Transport-Security",
		"Cache-Control",
		"Content-Security-Policy",
	} {
		if got := rec.Header().Get(h); got != "" {
			t.Errorf("%s = %q, want empty", h, got)
		}
	}
}
