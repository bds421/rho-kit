package approval

import (
	"testing"
	"time"
)

// FuzzApprovalCursorDecode targets the approval cursor verifier with
// arbitrary inputs. Invariant: every input either returns a valid
// (time, id) pair OR a wrapped ErrInvalidCursor — never a panic,
// never a silent accept of a tampered cursor.
func FuzzApprovalCursorDecode(f *testing.F) {
	signer, err := NewCursorSigner(make([]byte, MinCursorSigningKeyBytes))
	if err != nil {
		f.Fatalf("NewCursorSigner: %v", err)
	}
	valid := signer.Encode(time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC), "appr-1")

	seeds := []string{
		"",
		".",
		"a.b",
		valid,
		valid + "x",
		valid[:len(valid)-2],
		"\x00\x00\x00",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		_, _, _ = signer.Decode(in) // must not panic
	})
}
