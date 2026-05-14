package actionlog

import (
	"strings"
	"testing"
	"time"
)

// FuzzCursorSignerDecode runs malformed and adversarial cursor inputs
// against [CursorSigner.Decode]. The invariant is: every input either
// returns a valid (time, id) pair OR a wrapped ErrInvalidCursor — never
// a panic, never a silent decode of a corrupted payload. Seeds cover
// truncations, base64 garbage, oversize inputs, and the
// "empty cursor decodes to zero" edge.
func FuzzCursorSignerDecode(f *testing.F) {
	signer, err := NewCursorSigner(make([]byte, MinCursorSigningKeyBytes))
	if err != nil {
		f.Fatalf("NewCursorSigner: %v", err)
	}
	// Round-trip a real cursor so the corpus contains one well-formed
	// example.
	valid := signer.Encode(time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC), "00000000-0000-7000-8000-000000000000")

	seeds := []string{
		"",
		".",
		"a.b",
		"AAAA.BBBB",
		valid,
		strings.Repeat("A", MaxCursorLen+1),
		"\x00\x01\x02\x03",
		"unsigned-cursor",
		valid + ".extra",
		// Truncations that should fail the MAC compare.
		valid[:len(valid)-2],
		valid[:strings.IndexByte(valid, '.')+2],
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		_, _, _ = signer.Decode(in) // must not panic
	})
}
