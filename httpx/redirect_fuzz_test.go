package httpx

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzSafeRedirect feeds adversarial URL inputs to [SafeRedirect]. The
// invariant is: for every input, SafeRedirect either returns a string
// that is "safe" (relative to root OR pointing at an allow-listed host)
// OR returns an error. It must never panic and must never accept a
// scheme-relative, userinfo-bearing, control-character-carrying, or
// non-http(s) URL.
func FuzzSafeRedirect(f *testing.F) {
	seeds := []string{
		"/login",
		"//evil.example",
		"https://allowed.example/path",
		"javascript:alert(1)",
		"http://user:pass@allowed.example/",
		"\thttps://allowed.example",
		"https://allowed.example\\path",
		"https://%EF%BF%BD",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	allowed := []string{"allowed.example", "other.example:8443"}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := SafeRedirect(in, allowed...) // must not panic
		if err != nil {
			return
		}
		// On success the result MUST satisfy the safety invariant: it parses,
		// uses an http(s) or empty scheme, carries no userinfo, and is either
		// origin-relative (no host) or points at an allow-listed host:port.
		u, perr := url.Parse(out)
		if perr != nil {
			t.Fatalf("SafeRedirect returned unparseable %q for input %q: %v", out, in, perr)
		}
		if u.User != nil {
			t.Fatalf("SafeRedirect accepted userinfo: out=%q input=%q", out, in)
		}
		switch strings.ToLower(u.Scheme) {
		case "", "http", "https":
		default:
			t.Fatalf("SafeRedirect accepted non-http(s) scheme %q: out=%q input=%q", u.Scheme, out, in)
		}
		if u.Host == "" {
			// Origin-relative result: must not be scheme-relative ("//host").
			if strings.HasPrefix(out, "//") || strings.HasPrefix(out, "/\\") || strings.HasPrefix(out, "\\") {
				t.Fatalf("SafeRedirect accepted scheme-relative target: out=%q input=%q", out, in)
			}
			return
		}
		// Absolute result: host:port must be on the allowlist.
		target := strings.ToLower(u.Hostname())
		if p := u.Port(); p != "" {
			target += ":" + p
		}
		allowedSet := map[string]bool{
			"allowed.example":    true,
			"other.example:8443": true,
		}
		if !allowedSet[target] {
			t.Fatalf("SafeRedirect accepted off-allowlist host %q: out=%q input=%q", target, out, in)
		}
	})
}
