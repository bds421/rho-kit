package encrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// NewGCM creates an AES-256-GCM cipher from a 32-byte key.
// This is the shared primitive used by both [FieldEncryptor] (string-level)
// and storage encryption (byte-level).
//
// The key slice is copied internally. The internal copy is zeroed after
// the cipher is created; the caller's original is not modified.
func NewGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("encrypt: key must be 32 bytes, got %d", len(key))
	}
	// Copy the key so zeroing the internal copy doesn't affect the caller.
	keyCopy := make([]byte, 32)
	copy(keyCopy, key)

	block, err := aes.NewCipher(keyCopy)
	if err != nil {
		zeroBytes(keyCopy)
		return nil, fmt.Errorf("encrypt: create AES cipher: %w", err)
	}
	// Zero the copy — the AES block cipher has already expanded the key
	// into its internal round-key schedule.
	zeroBytes(keyCopy)

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("encrypt: create GCM: %w", err)
	}
	return gcm, nil
}

// NewGCMAndZeroKey creates an AES-256-GCM cipher and zeros the caller's key
// slice after copying it internally. Use this when the caller does not need
// to retain the key material after creating the cipher.
func NewGCMAndZeroKey(key []byte) (cipher.AEAD, error) {
	gcm, err := NewGCM(key)
	zeroBytes(key)
	return gcm, err
}

// zeroBytes overwrites a byte slice with zeros.
// Uses the clear builtin (Go 1.21+) which is not eliminated by compiler optimizations.
func zeroBytes(b []byte) {
	clear(b)
}

// SealBytes encrypts plaintext using AES-256-GCM and returns
// [nonce || ciphertext+tag]. A fresh random nonce is generated for each call.
func SealBytes(gcm cipher.AEAD, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("encrypt: generate nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// OpenBytes decrypts ciphertext produced by [SealBytes].
// Expects the format [nonce || ciphertext+tag].
func OpenBytes(gcm cipher.AEAD, ciphertext []byte) ([]byte, error) {
	nonceSize := gcm.NonceSize()
	// Ciphertext must contain at least nonce + authentication tag (overhead).
	// Without this, gcm.Open would receive an empty or too-short ciphertext
	// and return a generic decryption error instead of a clear size check.
	minLen := nonceSize + gcm.Overhead()
	if len(ciphertext) < minLen {
		return nil, errors.New("encrypt: ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return nil, fmt.Errorf("encrypt: decrypt: %w", err)
	}
	return plaintext, nil
}
