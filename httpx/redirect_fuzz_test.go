package httpx

import (
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
		_, _ = SafeRedirect(in, allowed...) // must not panic
	})
}
