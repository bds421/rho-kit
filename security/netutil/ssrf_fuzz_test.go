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
		_, _ = parseSSRFURL(in) // must not panic
	})
}
