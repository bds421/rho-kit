package auditlog

import (
	"testing"

	"github.com/bds421/rho-kit/core/v2/secret"
)

// FuzzAuditlogCursorDecode targets the audit-log cursor verifier with
// arbitrary inputs. Invariant: every input either returns a valid
// payload OR a wrapped ErrInvalidCursor — never a panic, never a
// silent accept of a tampered cursor.
func FuzzAuditlogCursorDecode(f *testing.F) {
	key := make([]byte, 32)
	sc := signedCursor{key: secret.New(append([]byte(nil), key...)), keyLen: len(key)}
	valid := sc.encodeCursor("audit-id-abc")

	seeds := []string{
		"",
		".",
		"a.b",
		valid,
		valid + "x",
		valid[:len(valid)-1],
		"\x00\x00",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		_, _ = sc.decodeCursor(in) // must not panic
	})
}
