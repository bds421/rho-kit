// asvs: V6.2.1, V6.4.1
package encrypt

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// encryptedV3Prefix is the current write format. Plain ASCII so the
// ciphertext stores cleanly in Postgres TEXT/VARCHAR columns and JSON
// strings. The actual security guarantee comes from AEAD verification
// in [FieldEncryptor.EncryptIfPlain] and [FieldEncryptor.DecryptWithContext],
// not the prefix shape — an attacker cannot construct a payload that
// decrypts cleanly without the key, regardless of how they frame
// the input.
const encryptedV3Prefix = "enc:v3:"

// legacyEncryptedV2Prefix is the previous wire format. Decrypt
// continues to accept it for read-only backward compatibility with
// rows written by kit v1 deployments — those bytes are valid AEAD
// ciphertext under the current key; only the framing prefix differs.
// The leading "\x00" was a defence-in-depth byte that broke Postgres
// TEXT/VARCHAR inserts (NUL bytes rejected as "invalid byte sequence
// for encoding UTF8"), which is why v3 dropped it.
//
// Encrypt never writes this prefix. Operators completing the v3
// migration may drop legacy reads in a future release once their
// stored data is fully re-encrypted.
const legacyEncryptedV2Prefix = "\x00enc:v2:"

// FieldEncryptor provides transparent AES-256-GCM encryption for
// database fields. The key must be exactly 32 bytes (256 bits).
//
// FieldEncryptor is safe for concurrent use by multiple goroutines.
// Do NOT copy a FieldEncryptor by value — always use a pointer
// (*FieldEncryptor).
//
// # Binding ciphertext to row context (AAD)
//
// Plain Encrypt/Decrypt produce ciphertext that is not bound to any
// out-of-band identifier — a row's encrypted value can be swapped into
// another row and decrypts cleanly. For database fields, prefer
// [FieldEncryptor.EncryptWithContext] / [FieldEncryptor.DecryptWithContext]
// and pass the row's stable primary key (or tenant ID + column name) as
// the AAD. The standard pattern:
//
//	aad := []byte("users:" + userID + ":email")
//	encrypted, err := enc.EncryptWithContext(plaintext, aad)
//	// later, on Decrypt, supply the same AAD:
//	plain, err := enc.DecryptWithContext(encrypted, aad)
type FieldEncryptor struct {
	aead AEAD
}

// NewFieldEncryptor creates a FieldEncryptor from a 32-byte key.
// Decrypt is strict: any value missing a recognised prefix (v3, or
// legacy v2 for read-only compatibility) is rejected with
// [ErrPlaintextNotAllowed]. There is no opt-in plaintext passthrough.
func NewFieldEncryptor(key []byte) (*FieldEncryptor, error) {
	a, err := NewGCM(key)
	if err != nil {
		return nil, err
	}
	return &FieldEncryptor{aead: a}, nil
}

// ErrPlaintextNotAllowed is returned by Decrypt when a value does not
// carry a recognised encryption prefix. Surfacing the error rather
// than silently passing the value through ensures stray plaintext
// writes (or upstream-component bypass attempts) become decryption
// failures instead of data leaks.
var ErrPlaintextNotAllowed = fmt.Errorf("encrypt: ciphertext missing %q prefix", encryptedV3Prefix)

// ErrInvalidEncryptor is returned when a FieldEncryptor was not
// constructed by NewFieldEncryptor.
var ErrInvalidEncryptor = fmt.Errorf("encrypt: FieldEncryptor is not initialized")

// Encrypt encrypts a plaintext string and returns a base64-encoded
// ciphertext prefixed with "enc:v3:". Empty strings are returned
// as-is. The output contains only printable ASCII, so it stores
// cleanly in Postgres TEXT/VARCHAR columns and JSON strings.
//
// Encrypt always produces fresh ciphertext, even if the input already
// looks like a previous Encrypt output. The "idempotent re-encrypt"
// shortcut available in pre-v2 versions allowed an attacker who could
// submit values into an encrypted field to bypass encryption with a
// known prefix. Use [FieldEncryptor.EncryptIfPlain] for the safe
// idempotent path — it AEAD-verifies a candidate ciphertext under the
// current key before treating it as already-encrypted.
func (e *FieldEncryptor) Encrypt(plaintext string) (string, error) {
	return e.EncryptWithContext(plaintext, nil)
}

// EncryptWithContext encrypts plaintext and binds the ciphertext to
// the supplied associated data (AAD). The AAD is authenticated but
// not encrypted; the same AAD must be supplied at Decrypt time. Pass
// a stable out-of-band identifier (row primary key, tenant ID +
// column name, etc.) to defeat ciphertext-substitution attacks where
// an attacker copies ciphertext from row A into row B.
//
// AAD nil is equivalent to [FieldEncryptor.Encrypt] (no binding).
func (e *FieldEncryptor) EncryptWithContext(plaintext string, aad []byte) (string, error) {
	if err := e.validate(); err != nil {
		return "", err
	}
	if plaintext == "" {
		return "", nil
	}
	sealed, err := EncryptBytesAAD(e.aead, []byte(plaintext), aad)
	if err != nil {
		return "", err
	}
	return encryptedV3Prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// EncryptIfPlain encrypts plaintext UNLESS it appears to already be
// valid ciphertext under the current key with no AAD. Use
// [FieldEncryptor.EncryptIfPlainWithContext] for AAD-bound fields.
//
// This is useful for idempotent "re-save the row" code paths where
// the same value may be passed through the encryptor repeatedly.
// Unlike a naive "Encrypt-with-prefix-shortcut", this verifies the
// candidate decrypts cleanly (AEAD tag valid) before treating it as
// already-encrypted — an attacker cannot bypass encryption by
// crafting a value that merely starts with the right prefix.
//
// Returns the input unchanged when it parses as a valid ciphertext
// for this key and nil AAD; otherwise encrypts.
func (e *FieldEncryptor) EncryptIfPlain(value string) (string, error) {
	return e.EncryptIfPlainWithContext(value, nil)
}

// EncryptIfPlainWithContext encrypts plaintext UNLESS it appears to
// already be valid ciphertext under the current key and the supplied
// AAD. Use this for idempotent "re-save the row" code paths when
// fields are encrypted with [FieldEncryptor.EncryptWithContext].
//
// Passing the same AAD used for EncryptWithContext preserves
// already-encrypted values unchanged. A ciphertext bound to different
// AAD is treated as plaintext and encrypted again, which prevents a
// copied value from bypassing the row/tenant binding.
func (e *FieldEncryptor) EncryptIfPlainWithContext(value string, aad []byte) (string, error) {
	if err := e.validate(); err != nil {
		return "", err
	}
	if value == "" {
		return "", nil
	}
	if e.isAuthenticatedCiphertext(value, aad) {
		return value, nil
	}
	return e.EncryptWithContext(value, aad)
}

// EncryptOptional encrypts the value with enc. A nil encryptor is a
// misconfiguration and returns ErrInvalidEncryptor; callers that
// intentionally store plaintext should make that branch explicit at
// the call site.
func EncryptOptional(enc *FieldEncryptor, value string) (string, error) {
	return EncryptOptionalWithContext(enc, value, nil)
}

// EncryptOptionalWithContext encrypts the value with enc and binds it
// to aad. A nil encryptor is a misconfiguration and returns
// ErrInvalidEncryptor; callers that intentionally store plaintext
// should make that branch explicit at the call site.
func EncryptOptionalWithContext(enc *FieldEncryptor, value string, aad []byte) (string, error) {
	if enc == nil {
		return "", ErrInvalidEncryptor
	}
	if value == "" {
		return "", nil
	}
	return enc.EncryptWithContext(value, aad)
}

// Decrypt decrypts a ciphertext string produced by [FieldEncryptor.Encrypt].
// Returns [ErrPlaintextNotAllowed] when the input lacks a recognised
// encryption prefix.
func (e *FieldEncryptor) Decrypt(ciphertext string) (string, error) {
	return e.DecryptWithContext(ciphertext, nil)
}

// DecryptWithContext decrypts ciphertext that was sealed with the
// same AAD. The AAD must match the EncryptWithContext call exactly;
// mismatch fails authentication and returns an error.
//
// Returns [ErrPlaintextNotAllowed] when the input lacks a recognised
// encryption prefix.
func (e *FieldEncryptor) DecryptWithContext(ciphertext string, aad []byte) (string, error) {
	if err := e.validate(); err != nil {
		return "", err
	}
	if ciphertext == "" {
		return "", nil
	}

	encoded, ok := stripEncryptedPrefix(ciphertext)
	if !ok {
		return "", ErrPlaintextNotAllowed
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	plaintext, err := DecryptBytesAAD(e.aead, data, aad)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// isAuthenticatedCiphertext returns true iff value parses as valid
// ciphertext for this encryptor's key with the given AAD. Used by
// [FieldEncryptor.EncryptIfPlain] to make the idempotent shortcut
// safe — only verifiable ciphertexts are passed through unchanged.
func (e *FieldEncryptor) isAuthenticatedCiphertext(value string, aad []byte) bool {
	if err := e.validate(); err != nil {
		return false
	}
	encoded, ok := stripEncryptedPrefix(value)
	if !ok {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	_, err = DecryptBytesAAD(e.aead, data, aad)
	return err == nil
}

func (e *FieldEncryptor) validate() error {
	if e == nil || e.aead == nil {
		return ErrInvalidEncryptor
	}
	return nil
}

// stripEncryptedPrefix returns the base64-encoded body of a
// recognised ciphertext and ok=true; ok=false for inputs without a
// recognised prefix. Accepts the current "enc:v3:" prefix and the
// legacy "\x00enc:v2:" prefix (read-only — Encrypt never writes the
// legacy form).
func stripEncryptedPrefix(s string) (string, bool) {
	switch {
	case strings.HasPrefix(s, encryptedV3Prefix):
		return strings.TrimPrefix(s, encryptedV3Prefix), true
	case strings.HasPrefix(s, legacyEncryptedV2Prefix):
		return strings.TrimPrefix(s, legacyEncryptedV2Prefix), true
	default:
		return "", false
	}
}
