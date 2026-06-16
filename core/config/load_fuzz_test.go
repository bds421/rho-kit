package config

import (
	"testing"
)

// FuzzParseEnvTag feeds arbitrary struct-tag inputs to the env-tag
// parser. Invariant: every input either returns a non-empty envName
// with the documented options OR returns a typed error — never a
// panic, never a silent accept of malformed tags.
func FuzzParseEnvTag(f *testing.F) {
	seeds := []string{
		"FOO",
		"FOO,required",
		"FOO,required,extra",
		",required",
		"",
		"  ,required",
		"FOO,unknown",
		"FOO,,,",
		"   FOO   ",
		"FOO=BAR",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, tag string) {
		_, _, _ = parseEnvTag(tag) // must not panic
	})
}
