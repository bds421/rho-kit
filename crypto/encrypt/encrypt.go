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
// Already-encrypted values (v1 or v2 prefix) are returned unchanged.
func (e *FieldEncryptor) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if isEncrypted(plaintext) {
		return plaintext, nil
	}

	sealed, err := SealBytes(e.gcm, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return encryptedV2Prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// isEncrypted checks for both v1 (legacy) and v2 prefix.
func isEncrypted(s string) bool {
	return strings.HasPrefix(s, encryptedV2Prefix) || strings.HasPrefix(s, encryptedV1Prefix)
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
	if ciphertext == "" {
		return "", nil
	}

	var encoded string
	switch {
	case strings.HasPrefix(ciphertext, encryptedV2Prefix):
		encoded = strings.TrimPrefix(ciphertext, encryptedV2Prefix)
	case strings.HasPrefix(ciphertext, encryptedV1Prefix):
		encoded = strings.TrimPrefix(ciphertext, encryptedV1Prefix)
	default:
		return ciphertext, nil
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	plaintext, err := OpenBytes(e.gcm, data)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}
