package encrypt

import (
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"strings"
)

// encryptedV2Prefix marks versioned ciphertext with a null byte prefix
// that cannot appear in valid UTF-8 user input, preventing collision
// with legitimate plaintext. New encryptions use v2.
const encryptedV2Prefix = "\x00enc:v2:"

// encryptedV1Prefix is the legacy prefix without null byte guard.
// Decrypt still accepts it for backward compatibility.
const encryptedV1Prefix = "enc:v1:"

// FieldEncryptor provides transparent AES-256-GCM encryption for database fields.
// The key must be exactly 32 bytes (256 bits).
//
// FieldEncryptor is safe for concurrent use by multiple goroutines. Each call
// to Encrypt generates a fresh random nonce stack-locally, and cipher.AEAD's
// Seal/Open methods are documented as safe for concurrent use.
// Do NOT copy a FieldEncryptor by value — always use a pointer (*FieldEncryptor).
//
// # Binding ciphertext to row context (AAD)
//
// Plain Encrypt/Decrypt produce ciphertext that is not bound to any
// out-of-band identifier — a row's encrypted value can be swapped into
// another row and decrypts cleanly. For database fields, prefer
// [FieldEncryptor.EncryptWithContext] / [FieldEncryptor.DecryptWithContext]
// and pass the row's stable primary key (or tenant ID + column name) as the
// AAD. The standard pattern:
//
//	aad := []byte("users:" + userID + ":email")
//	encrypted, err := enc.EncryptWithContext(plaintext, aad)
//	// later, on Decrypt, supply the same AAD:
//	plain, err := enc.DecryptWithContext(encrypted, aad)
type FieldEncryptor struct {
	gcm cipher.AEAD
}

// NewFieldEncryptor creates a FieldEncryptor from a 32-byte key.
func NewFieldEncryptor(key []byte) (*FieldEncryptor, error) {
	gcm, err := NewGCM(key)
	if err != nil {
		return nil, err
	}
	return &FieldEncryptor{gcm: gcm}, nil
}

// Encrypt encrypts a plaintext string and returns a base64-encoded ciphertext
// prefixed with "\x00enc:v2:". Empty strings are returned as-is.
//
// CHANGED in v1.x: Encrypt no longer returns the input unchanged when it
// already begins with an encrypted-prefix. The previous "idempotent"
// behavior allowed an attacker who controls a value submitted into an
// encrypted field to bypass encryption with a 7-byte prefix. Callers that
// need the safe idempotent shortcut (e.g. re-saving a row whose value may
// already be encrypted) should use [FieldEncryptor.EncryptIfPlain], which
// AEAD-verifies a candidate ciphertext under the current key before
// treating it as already-encrypted.
func (e *FieldEncryptor) Encrypt(plaintext string) (string, error) {
	return e.EncryptWithContext(plaintext, nil)
}

// EncryptWithContext encrypts plaintext and binds the ciphertext to the
// supplied associated data (AAD). The AAD is authenticated but not
// encrypted; the same AAD must be supplied at Decrypt time. Pass a stable
// out-of-band identifier (row primary key, tenant ID + column name, etc.)
// to defeat ciphertext-substitution attacks where an attacker copies
// ciphertext from row A into row B.
//
// AAD nil is equivalent to [FieldEncryptor.Encrypt] (no binding).
func (e *FieldEncryptor) EncryptWithContext(plaintext string, aad []byte) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	sealed, err := SealBytesAAD(e.gcm, []byte(plaintext), aad)
	if err != nil {
		return "", err
	}
	return encryptedV2Prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// EncryptIfPlain encrypts plaintext UNLESS it appears to already be valid
// ciphertext under the current key. Use this for idempotent "re-save the
// row" code paths where the same value may be passed through the encryptor
// repeatedly. Unlike the previous Encrypt-with-prefix-shortcut, this
// verifies the candidate decrypts cleanly (AEAD tag valid) before treating
// it as already-encrypted — an attacker cannot bypass encryption by
// crafting a value that merely starts with the right prefix.
//
// Returns the input unchanged when it parses as a valid ciphertext for
// this key; otherwise encrypts.
func (e *FieldEncryptor) EncryptIfPlain(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if e.isAuthenticatedCiphertext(value, nil) {
		return value, nil
	}
	return e.Encrypt(value)
}

// EncryptOptional encrypts the value if enc is non-nil, otherwise returns it unchanged.
// This eliminates the repetitive nil-check pattern at every call site.
func EncryptOptional(enc *FieldEncryptor, value string) (string, error) {
	if enc == nil || value == "" {
		return value, nil
	}
	return enc.Encrypt(value)
}

// Decrypt decrypts a ciphertext string produced by Encrypt.
// Values without a recognized prefix are returned as-is (plaintext passthrough).
// Supports both v1 (legacy "enc:v1:") and v2 ("\x00enc:v2:") formats.
func (e *FieldEncryptor) Decrypt(ciphertext string) (string, error) {
	return e.DecryptWithContext(ciphertext, nil)
}

// DecryptWithContext decrypts ciphertext that was sealed with the same AAD.
// The AAD must match the EncryptWithContext call exactly; mismatch fails
// authentication and returns an error.
//
// Values without a recognized prefix are returned as-is (plaintext
// passthrough — preserves backward compat for legacy unencrypted rows).
func (e *FieldEncryptor) DecryptWithContext(ciphertext string, aad []byte) (string, error) {
	if ciphertext == "" {
		return "", nil
	}

	encoded, ok := stripEncryptedPrefix(ciphertext)
	if !ok {
		return ciphertext, nil
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	plaintext, err := OpenBytesAAD(e.gcm, data, aad)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// isAuthenticatedCiphertext returns true iff value parses as valid
// ciphertext for this encryptor's key with the given AAD. Used by
// [FieldEncryptor.EncryptIfPlain] to make the idempotent shortcut safe —
// only verifiable ciphertexts are passed through unchanged.
func (e *FieldEncryptor) isAuthenticatedCiphertext(value string, aad []byte) bool {
	encoded, ok := stripEncryptedPrefix(value)
	if !ok {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	_, err = OpenBytesAAD(e.gcm, data, aad)
	return err == nil
}

// stripEncryptedPrefix returns the base64-encoded body of a v1 or v2 prefix
// ciphertext and ok=true; ok=false for inputs without a recognised prefix.
func stripEncryptedPrefix(s string) (string, bool) {
	switch {
	case strings.HasPrefix(s, encryptedV2Prefix):
		return strings.TrimPrefix(s, encryptedV2Prefix), true
	case strings.HasPrefix(s, encryptedV1Prefix):
		return strings.TrimPrefix(s, encryptedV1Prefix), true
	default:
		return "", false
	}
}
