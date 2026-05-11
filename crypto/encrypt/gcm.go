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

// NewGCMAndZeroKey creates an AES-256-GCM AEAD and zeros the
// caller's key slice after Tink has copied it internally. Use this
// when the caller does not need to retain the key material after
// constructing the cipher.
func NewGCMAndZeroKey(key []byte) (AEAD, error) {
	a, err := NewGCM(key)
	zeroBytes(key)
	return a, err
}

// zeroBytes overwrites a byte slice with zeros.
// Uses the clear builtin (Go 1.21+) which is not eliminated by compiler optimizations.
func zeroBytes(b []byte) {
	clear(b)
}

// SealBytes encrypts plaintext using AES-256-GCM and returns
// "iv ‖ ciphertext ‖ tag". A fresh random IV is generated per call.
// Equivalent to [SealBytesAAD] with nil AAD.
func SealBytes(a AEAD, plaintext []byte) ([]byte, error) {
	return SealBytesAAD(a, plaintext, nil)
}

// SealBytesAAD encrypts plaintext with associated data (AAD). The
// AAD is authenticated but not encrypted — it must be supplied
// identically at Open time. Use this to bind ciphertext to a stable
// out-of-band identifier (row primary key, tenant ID, file path) so
// a ciphertext copy-pasted into a different row fails authentication.
func SealBytesAAD(a AEAD, plaintext, aad []byte) ([]byte, error) {
	if a == nil {
		return nil, ErrInvalidAEAD
	}
	out, err := a.Encrypt(plaintext, aad)
	if err != nil {
		return nil, fmt.Errorf("encrypt: seal: %w", err)
	}
	return out, nil
}

// OpenBytes decrypts ciphertext produced by [SealBytes].
// Equivalent to [OpenBytesAAD] with nil AAD.
func OpenBytes(a AEAD, ciphertext []byte) ([]byte, error) {
	return OpenBytesAAD(a, ciphertext, nil)
}

// OpenBytesAAD decrypts ciphertext produced by [SealBytesAAD]. The
// AAD must match the value supplied at Seal time exactly; mismatch
// produces an authentication error indistinguishable from a tampered
// ciphertext.
func OpenBytesAAD(a AEAD, ciphertext, aad []byte) ([]byte, error) {
	if a == nil {
		return nil, ErrInvalidAEAD
	}
	plaintext, err := a.Decrypt(ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("encrypt: decrypt: %w", err)
	}
	return plaintext, nil
}
