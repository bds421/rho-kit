package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrEmptySecret is returned when an empty or too-short secret is passed to Sign or Verify.
var ErrEmptySecret = errors.New("signing: secret must be at least 32 bytes")

// minSecretLen is the minimum secret length for HMAC-SHA256 (matches hash output size).
const minSecretLen = 32

// ErrExpiredSignature is returned by Verify when the signature timestamp
// exceeds maxAge or is too far in the future (beyond the allowed skew).
var ErrExpiredSignature = errors.New("signing: signature expired or clock skew too large")

// Signer holds configuration for computing and verifying HMAC-SHA256 signatures.
type Signer struct {
	clock func() time.Time
}

// SignerOption configures a Signer.
type SignerOption func(*Signer)

// WithClock sets the time source for signing. Useful for deterministic testing.
func WithClock(fn func() time.Time) SignerOption {
	return func(s *Signer) { s.clock = fn }
}

// NewSigner creates a Signer with the given options.
func NewSigner(opts ...SignerOption) *Signer {
	s := &Signer{clock: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Sign computes an HMAC-SHA256 signature using the Signer's clock.
func (s *Signer) Sign(body []byte, secret []byte) (signature string, timestamp int64, err error) {
	if len(secret) < minSecretLen {
		return "", 0, ErrEmptySecret
	}
	timestamp = s.clock().Unix()
	payload := fmt.Appendf(nil, "%d.", timestamp)
	payload = append(payload, body...)
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil)), timestamp, nil
}

// defaultSigner is the package-level Signer using time.Now as clock source.
var defaultSigner = NewSigner()

// Sign computes an HMAC-SHA256 signature for the given body and secret,
// including a Unix timestamp in the signed payload to prevent replay attacks.
// The signed payload is: "<timestamp>.<body>".
//
// Returns [ErrEmptySecret] if secret is empty.
//
// Note: No nonce is included — within the maxAge window, a valid signature can
// be replayed. This follows the same model as Stripe webhook signatures.
// Callers requiring within-window replay prevention must maintain a
// seen-timestamp/nonce set externally.
func Sign(body []byte, secret []byte) (signature string, timestamp int64, err error) {
	return defaultSigner.Sign(body, secret)
}

// DefaultSignatureMaxAge is the default maximum age for webhook signatures.
const DefaultSignatureMaxAge = 5 * time.Minute

// allowedFutureSkew tolerates small clock differences where the sender's
// clock is slightly ahead of the receiver's. Without this, NTP jitter
// causes spurious signature verification failures.
const allowedFutureSkew = 30 * time.Second

// fallbackMAC is a pre-allocated zero buffer matching SHA-256 output size.
// Used as the comparison operand when the signature has a format error (missing
// prefix, invalid hex), ensuring constant-time comparison runs regardless of
// input validity — this prevents timing side-channels that distinguish format
// errors from HMAC mismatches. Declared as a fixed-size array so it cannot be
// accidentally mutated (slicing creates a copy header).
var fallbackMAC [sha256.Size]byte

// Verify checks an HMAC-SHA256 signature using the Signer's clock for age
// calculation (enabling deterministic testing). Uses constant-time comparison.
// The timestamp and body are combined as "<timestamp>.<body>" to match Sign.
// It rejects signatures older than maxAge to limit the replay window.
// A small future clock skew (30s) is tolerated for NTP jitter.
// Use DefaultSignatureMaxAge for a reasonable default.
//
// Returns [ErrEmptySecret] if secret is empty.
func (s *Signer) Verify(secret []byte, body []byte, timestamp int64, signature string, maxAge time.Duration) (bool, error) {
	if len(secret) < minSecretLen {
		return false, ErrEmptySecret
	}
	age := s.clock().Sub(time.Unix(timestamp, 0))
	if age < -allowedFutureSkew || age > maxAge {
		return false, ErrExpiredSignature
	}

	payload := fmt.Appendf(nil, "%d.", timestamp)
	payload = append(payload, body...)
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	expectedRaw := mac.Sum(nil)

	// Always decode and compare in a single code path regardless of format
	// errors. This eliminates timing differences between format errors and
	// HMAC mismatches.
	const prefix = "sha256="
	gotRaw := fallbackMAC[:]
	if len(signature) >= len(prefix) && signature[:len(prefix)] == prefix {
		if decoded, err := hex.DecodeString(signature[len(prefix):]); err == nil {
			gotRaw = decoded
		}
	}
	return hmac.Equal(expectedRaw, gotRaw), nil
}

// Verify checks an HMAC-SHA256 signature using constant-time comparison.
// The timestamp and body are combined as "<timestamp>.<body>" to match Sign.
// It rejects signatures older than maxAge to limit the replay window.
// A small future clock skew (30s) is tolerated for NTP jitter.
// Use DefaultSignatureMaxAge for a reasonable default.
//
// Returns [ErrEmptySecret] if secret is empty.
func Verify(secret []byte, body []byte, timestamp int64, signature string, maxAge time.Duration) (bool, error) {
	return defaultSigner.Verify(secret, body, timestamp, signature, maxAge)
}
