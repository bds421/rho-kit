package approval

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/secret"
)

// ErrInvalidCursor is returned by [Store.List] when the supplied
// [Query.Cursor] is malformed, signed by a different key, or otherwise
// fails the HMAC verification step. Callers should map it to 400 Bad
// Request and restart pagination rather than retrying — every retry
// will fail the same way.
var ErrInvalidCursor = errors.New("approval: query cursor is invalid")

// MaxCursorLen caps the encoded cursor length the signer will accept
// before base64-decoding. Mirrors actionlog.MaxCursorLen — see that
// constant for rationale.
const MaxCursorLen = 4096

// MinCursorSigningKeyBytes is the minimum acceptable HMAC key length
// for the cursor signer. 32 bytes matches HMAC-SHA256's output size
// and the auditlog/cursor floor.
const MinCursorSigningKeyBytes = 32

// CursorSigner produces HMAC-SHA256-signed keyset cursors and
// constant-time-verifies them on read. Construction binds a per-deployment
// signing key so callers cannot forge cursors to skip ahead through
// pending-approval pages and observe entries that should still be queued.
//
// Wire format mirrors the auditlog and actionlog cursor signers:
//
//	signedCursor = base64url(payload) "." base64url(HMAC-SHA256(key, payload))
//
// Safe for concurrent use after construction.
type CursorSigner struct {
	key    *secret.String
	keyLen int
}

// NewCursorSigner builds a CursorSigner from a HMAC signing key.
// Key bytes are copied into a [secret.String] so callers can zero
// their source slice immediately after construction. Returns an
// error if the key is shorter than [MinCursorSigningKeyBytes].
func NewCursorSigner(key []byte) (*CursorSigner, error) {
	if len(key) < MinCursorSigningKeyBytes {
		return nil, fmt.Errorf("approval: cursor signing key must be at least %d bytes", MinCursorSigningKeyBytes)
	}
	return &CursorSigner{
		key:    secret.New(append([]byte(nil), key...)),
		keyLen: len(key),
	}, nil
}

// Encode renders the keyset position (createdAt, id) as a signed,
// URL-safe string. Returns "" when id is empty — store implementations
// pass "" to indicate "no more pages" so the empty case must round-trip
// cleanly without a signature.
func (s *CursorSigner) Encode(createdAt time.Time, id string) string {
	if s == nil || id == "" {
		return ""
	}
	payload := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id
	var sum []byte
	s.key.Use(func(k []byte) {
		mac := hmac.New(sha256.New, k)
		mac.Write([]byte(payload))
		sum = mac.Sum(nil)
	})
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sum)
}

// Decode verifies and decodes a cursor produced by [CursorSigner.Encode].
// An empty cursor decodes to (zero time, ""), which stores treat as
// "start from the head". Every other malformed, oversized, or
// tamper-detected input returns a wrapped [ErrInvalidCursor].
func (s *CursorSigner) Decode(cursor string) (time.Time, string, error) {
	if cursor == "" {
		return time.Time{}, "", nil
	}
	if s == nil || s.keyLen == 0 || s.key == nil || s.key.IsEmpty() {
		return time.Time{}, "", fmt.Errorf("%w: cursor signer is not configured", ErrInvalidCursor)
	}
	if len(cursor) > MaxCursorLen {
		return time.Time{}, "", fmt.Errorf("%w: cursor exceeds maximum length", ErrInvalidCursor)
	}
	idx := strings.IndexByte(cursor, '.')
	if idx < 0 {
		return time.Time{}, "", fmt.Errorf("%w: cursor is malformed", ErrInvalidCursor)
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor[:idx])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: cursor payload is not base64url", ErrInvalidCursor)
	}
	sig, err := base64.RawURLEncoding.DecodeString(cursor[idx+1:])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: cursor signature is not base64url", ErrInvalidCursor)
	}
	var match bool
	s.key.Use(func(k []byte) {
		expected := hmac.New(sha256.New, k)
		expected.Write(payload)
		match = subtle.ConstantTimeCompare(sig, expected.Sum(nil)) == 1
	})
	if !match {
		return time.Time{}, "", fmt.Errorf("%w: cursor signature does not verify", ErrInvalidCursor)
	}
	sep := strings.IndexByte(string(payload), '|')
	if sep <= 0 || sep == len(payload)-1 {
		return time.Time{}, "", fmt.Errorf("%w: cursor payload is malformed", ErrInvalidCursor)
	}
	ts, err := time.Parse(time.RFC3339Nano, string(payload[:sep]))
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: cursor timestamp is malformed", ErrInvalidCursor)
	}
	return ts.UTC(), string(payload[sep+1:]), nil
}
