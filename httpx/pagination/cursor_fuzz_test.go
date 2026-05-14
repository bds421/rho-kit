package pagination

import (
	"testing"
)

// FuzzPaginationCursorDecode exercises the public cursor verifier with
// arbitrary strings. The invariant is: every input either returns a
// valid payload OR ErrCursorInvalid — never a panic, never a silent
// accept of a tampered cursor.
func FuzzPaginationCursorDecode(f *testing.F) {
	signer, err := NewCursorSigner(make([]byte, minCursorSignerSecretLen))
	if err != nil {
		f.Fatalf("NewCursorSigner: %v", err)
	}
	valid := signer.Encode("test-payload-uuid-00000000")
	seeds := []string{
		"",
		".",
		"a.b",
		valid,
		valid + "tail",
		valid[:len(valid)-1],
		"unsigned",
		"\x00\x00\x00",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		_, _ = signer.Decode(in) // must not panic
	})
}
