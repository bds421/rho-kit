// asvs: V13.4.1
package auditlog

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/bds421/rho-kit/core/v2/secret"
)

// ErrInvalidCursor is returned by [Logger.List] when a supplied cursor is
// malformed, truncated, signed by a different key, or otherwise fails the
// HMAC verification step. Callers can use errors.Is(err, ErrInvalidCursor)
// to distinguish forgery / tamper attempts from Store I/O failures and
// translate to 400 Bad Request at the HTTP boundary.
var ErrInvalidCursor = errors.New("auditlog: invalid or tampered cursor")

// signedCursor wraps a raw cursor string with an HMAC tag so attackers
// cannot forge / enumerate cursors. On-wire format mirrors
// httpx/pagination.CursorSigner so the two implementations behave
// consistently (the observability module cannot depend on httpx, hence
// the inline duplication).
//
//	signedCursor = base64url(payload) "." base64url(HMAC-SHA256(key, payload))
//
// The HMAC binds the payload bytes to a specific deployment-wide key.
// Comparing the HMAC in constant time prevents timing oracles on the
// verify path.
type signedCursor struct {
	// key wraps the HMAC signing material in [secret.String] so the
	// raw bytes can be zeroed at [Logger.Close]. Reveals are bounded
	// by [secret.String.Use] closures inside encode/decode.
	key    *secret.String
	keyLen int
}

// encodeCursor signs the raw cursor payload. Returns "" for empty
// payloads (i.e. no next page). Returns "" if the signer is nil or has
// no key — but callers should never invoke this path; [Logger]
// constructs the signer with a validated key at startup.
func (s signedCursor) encodeCursor(payload string) string {
	if payload == "" || s.keyLen == 0 || s.key == nil || s.key.IsEmpty() {
		return ""
	}
	var sum []byte
	s.key.Use(func(k []byte) {
		mac := hmac.New(sha256.New, k)
		mac.Write([]byte(payload))
		sum = mac.Sum(nil)
	})
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sum)
}

// decodeCursor verifies and decodes a signed cursor produced by
// [signedCursor.encodeCursor]. Returns "" with nil error for the empty
// (first-page) cursor. Returns a wrapped [ErrInvalidCursor] for any
// malformed, truncated, or tampered input — so callers in the kit's HTTP
// handlers can errors.Is(err, ErrInvalidCursor) and map cleanly to 400
// Bad Request.
func (s signedCursor) decodeCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if s.keyLen == 0 || s.key == nil || s.key.IsEmpty() {
		return "", fmt.Errorf("%w: cursor signer is not configured", ErrInvalidCursor)
	}
	idx := strings.IndexByte(cursor, '.')
	if idx < 0 {
		return "", fmt.Errorf("%w: cursor is malformed", ErrInvalidCursor)
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor[:idx])
	if err != nil {
		return "", fmt.Errorf("%w: cursor payload is not base64url", ErrInvalidCursor)
	}
	sig, err := base64.RawURLEncoding.DecodeString(cursor[idx+1:])
	if err != nil {
		return "", fmt.Errorf("%w: cursor signature is not base64url", ErrInvalidCursor)
	}
	var match bool
	s.key.Use(func(k []byte) {
		expected := hmac.New(sha256.New, k)
		expected.Write(payload)
		match = subtle.ConstantTimeCompare(sig, expected.Sum(nil)) == 1
	})
	if !match {
		return "", fmt.Errorf("%w: cursor signature does not verify", ErrInvalidCursor)
	}
	return string(payload), nil
}
