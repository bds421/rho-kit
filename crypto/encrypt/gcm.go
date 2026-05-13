package encrypt

import (
	"errors"
	"fmt"

	"github.com/tink-crypto/tink-go/v2/aead/subtle"
	"github.com/tink-crypto/tink-go/v2/tink"
)

// AEAD is the kit's authenticated-encryption interface — a thin
// re-export of [tink.AEAD] so callers don't need to import Tink to
// hold an encryptor. The type identity is the same as Tink's, so a
// value returned by [NewGCM] satisfies any function signature that
// expects either form.
//
// Encrypt(plaintext, associatedData) returns "iv ‖ ciphertext ‖ tag"
// in RFC 5116 §5.1 layout. Decrypt expects the same layout. There is
// no Tink output prefix — the on-disk format is byte-identical to the
// stdlib cipher.AEAD layout the kit shipped before v2.
type AEAD = tink.AEAD

// ErrInvalidAEAD is returned when a caller invokes the byte-level
// helpers without a constructed AEAD primitive.
var ErrInvalidAEAD = errors.New("encrypt: AEAD must not be nil")

// NewGCM creates an AES-256-GCM AEAD primitive from a 32-byte key,
// backed by Google Tink. The wire format ("iv ‖ ct ‖ tag", 12-byte
// IV, 16-byte tag) matches the stdlib cipher.AEAD output the kit
// shipped before v2 — existing ciphertext decrypts unchanged.
//
// The caller's key slice is copied internally; the caller may zero
// or reuse its slice immediately after this call returns.
func NewGCM(key []byte) (AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("encrypt: key must be 32 bytes")
	}
	a, err := subtle.NewAESGCM(key)
	if err != nil {
		return nil, fmt.Errorf("encrypt: create AES-256-GCM: %w", err)
	}
	return a, nil
}

// EncryptBytes encrypts plaintext using AES-256-GCM and returns
// "iv ‖ ciphertext ‖ tag". A fresh random IV is generated per call.
// Equivalent to [EncryptBytesAAD] with nil AAD.
//
// Operational ceiling: random 96-bit IVs reach a non-trivial collision
// probability after ≈ 2^32 encryptions under one key (NIST SP 800-38D
// §8.3). When driving the AEAD through this helper directly, operators
// have no per-key op counter — wrap the AEAD in [FieldEncryptor] (which
// exposes [FieldEncryptor.OpsCount]) when the deployment will exceed
// ~10^9 ops per key lifetime.
func EncryptBytes(a AEAD, plaintext []byte) ([]byte, error) {
	return EncryptBytesAAD(a, plaintext, nil)
}

// EncryptBytesAAD encrypts plaintext with associated data (AAD). The
// AAD is authenticated but not encrypted — it must be supplied
// identically at Decrypt time. Use this to bind ciphertext to a stable
// out-of-band identifier (row primary key, tenant ID, file path) so
// a ciphertext copy-pasted into a different row fails authentication.
func EncryptBytesAAD(a AEAD, plaintext, aad []byte) ([]byte, error) {
	if a == nil {
		return nil, ErrInvalidAEAD
	}
	out, err := a.Encrypt(plaintext, aad)
	if err != nil {
		return nil, fmt.Errorf("encrypt: encrypt bytes: %w", err)
	}
	return out, nil
}

// DecryptBytes decrypts ciphertext produced by [EncryptBytes].
// Equivalent to [DecryptBytesAAD] with nil AAD.
func DecryptBytes(a AEAD, ciphertext []byte) ([]byte, error) {
	return DecryptBytesAAD(a, ciphertext, nil)
}

// DecryptBytesAAD decrypts ciphertext produced by [EncryptBytesAAD].
// The AAD must match the value supplied at Encrypt time exactly;
// mismatch produces an authentication error indistinguishable from
// a tampered ciphertext.
func DecryptBytesAAD(a AEAD, ciphertext, aad []byte) ([]byte, error) {
	if a == nil {
		return nil, ErrInvalidAEAD
	}
	plaintext, err := a.Decrypt(ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("encrypt: decrypt bytes: %w", err)
	}
	return plaintext, nil
}
