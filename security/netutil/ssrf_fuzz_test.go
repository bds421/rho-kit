package netutil

import (
	"testing"
)

// FuzzParseSSRFURL exercises the URL parser the SSRF-safe-from-URL
// constructors call before any dial happens. Invariant: every input
// either yields a *url.URL with non-empty scheme + hostname (the
// outer constructor still validates IP/host) OR returns an error.
// No panic, no silent accept of empty-host / non-http / userinfo URLs.
func FuzzParseSSRFURL(f *testing.F) {
	seeds := []string{
		"http://example.com",
		"https://[::1]/",
		"http://",
		"http://user:pass@example.com",
		"file:///etc/passwd",
		"gopher://example.com:70",
		"http://example.com:notaport",
		"%00",
		"http://example.com/path?q=1#frag",
		"javascript:alert(1)",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		u, err := parseSSRFURL(in)
		if err != nil {
			return // documented contract: any rejected input returns an error
		}
		// On success the parser must have produced a usable URL: a
		// non-nil *url.URL whose scheme is http/https and whose host is
		// non-empty. A regression that returned (e.g.) "http://" with a
		// nil error — the exact empty-host footgun parseSSRFURL exists to
		// prevent — must fail here rather than slip through to a dial.
		if u == nil {
			t.Fatalf("parseSSRFURL(%q) returned nil URL with nil error", in)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			t.Fatalf("parseSSRFURL(%q) accepted non-http(s) scheme %q", in, u.Scheme)
		}
		if u.Hostname() == "" {
			t.Fatalf("parseSSRFURL(%q) accepted empty host", in)
		}
		if u.User != nil {
			t.Fatalf("parseSSRFURL(%q) accepted userinfo", in)
		}
	})
}
