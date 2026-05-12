// Package kekstatic provides an in-memory [envelope.KEK] implementation
// suitable for tests and development. Each named key wraps DEKs with
// AES-256-GCM. Production deployments should use a managed KEK adapter such as
// awskms, azurekeyvault, gcpkms, or vaulttransit, or implement the
// envelope.KEK interface against their provider's KMS SDK.
//
// Static KEKs are NOT a substitute for a KMS:
//
//   - The master key lives in process memory; a heap dump leaks every
//     ciphertext written under it.
//   - There is no audit trail.
//   - There is no enforced rotation cadence.
//
// Use kekstatic for unit tests, integration tests with deterministic fixtures,
// and local-dev workflows.
package kekstatic

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"unicode"
	"unicode/utf8"
)

// KEK is an in-memory KEK that holds one or more named keys. The
// "current" key (selected at construction or via [KEK.Rotate]) is
// returned by [KEK.KeyID] and used by [KEK.Wrap]; older keys remain
// available for [KEK.Unwrap] until removed.
type KEK struct {
	mu      sync.RWMutex
	keys    map[string][]byte
	current string
}

// NewKEK returns a KEK with the given keyID active and no other keys
// available. masterKey must be 32 bytes (AES-256).
func NewKEK(keyID string, masterKey []byte) (*KEK, error) {
	if err := validateKeyID(keyID); err != nil {
		return nil, err
	}
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("kekstatic: masterKey must be 32 bytes")
	}
	k := &KEK{
		keys:    map[string][]byte{keyID: append([]byte(nil), masterKey...)},
		current: keyID,
	}
	return k, nil
}

// AddKey registers an additional master key under keyID. The new key is
// not made active — callers can [KEK.Rotate] to switch when ready.
func (k *KEK) AddKey(keyID string, masterKey []byte) error {
	if k == nil {
		return errors.New("kekstatic: KEK must not be nil")
	}
	if err := validateKeyID(keyID); err != nil {
		return err
	}
	if len(masterKey) != 32 {
		return fmt.Errorf("kekstatic: masterKey must be 32 bytes")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys == nil {
		k.keys = make(map[string][]byte)
	}
	if _, exists := k.keys[keyID]; exists {
		return fmt.Errorf("kekstatic: keyID already registered")
	}
	k.keys[keyID] = append([]byte(nil), masterKey...)
	return nil
}

// Rotate makes keyID the active key. The keyID must already be
// registered via [New] or [KEK.AddKey]. Subsequent Wrap calls embed
// the new keyID; existing blobs continue to decrypt via the older key
// until they are rewrapped.
func (k *KEK) Rotate(keyID string) error {
	if k == nil {
		return errors.New("kekstatic: KEK must not be nil")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, exists := k.keys[keyID]; !exists {
		return fmt.Errorf("kekstatic: keyID not registered")
	}
	k.current = keyID
	return nil
}

// RemoveKey deletes the named key. Use only after every blob written
// under it has been rewrapped. Returns an error if asked to remove the
// active key (the caller is expected to rotate first).
//
// Earlier versions panicked on active-key removal, which crashed any
// config-reload diff that compared old and new keysets and called
// RemoveKey on each removed entry without first rotating.
func (k *KEK) RemoveKey(keyID string) error {
	if k == nil {
		return errors.New("kekstatic: KEK must not be nil")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.current == keyID {
		return fmt.Errorf("kekstatic: cannot remove the active keyID; rotate to a different active key first")
	}
	if _, exists := k.keys[keyID]; !exists {
		return fmt.Errorf("kekstatic: keyID not registered")
	}
	zeroBytes(k.keys[keyID])
	delete(k.keys, keyID)
	return nil
}

// KeyID returns the active key identifier.
func (k *KEK) KeyID() string {
	if k == nil {
		return ""
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.current
}

// Wrap encrypts dek under the active master key with AES-256-GCM. The
// returned blob is `nonce(12) || ciphertext+tag`. The active keyID is
// bound as AEAD AAD so a wrapped DEK can only be opened under the
// exact keyID it was wrapped with — even if the same master bytes are
// registered under multiple ids, swapping the envelope's keyID header
// to an alternate id fails to authenticate.
//
// keyID and master are snapshotted under the same RLock so the
// returned (keyID, wrapped) pair is internally consistent even if a
// concurrent Rotate happens between the snapshot and the seal. The
// caller (envelope.Encrypt / envelope.Rewrap) must use the returned
// keyID for the envelope header — never KEK.KeyID() — to avoid a
// rotation race that would record a header keyID different from the
// one bound as AAD.
func (k *KEK) Wrap(_ context.Context, dek []byte) (string, []byte, error) {
	if k == nil {
		return "", nil, errors.New("kekstatic: KEK must not be nil")
	}
	// Build the GCM under the lock so a concurrent RemoveKey or future
	// zero-on-removal hardening cannot mutate the slice while we use it.
	k.mu.RLock()
	keyID := k.current
	master := k.keys[keyID]
	if master == nil {
		k.mu.RUnlock()
		return "", nil, errors.New("kekstatic: no active key")
	}
	gcm, err := newGCM(master)
	k.mu.RUnlock()
	if err != nil {
		return "", nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, fmt.Errorf("kekstatic: read nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, dek, []byte(keyID))

	out := make([]byte, 0, gcm.NonceSize()+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return keyID, out, nil
}

// Unwrap decrypts wrapped under the named keyID. Returns an error if
// the keyID is not registered (rejected explicitly rather than silently
// trying the active key). The keyID is bound as AEAD AAD; mismatched
// keyIDs fail authentication even when the underlying master bytes
// match.
func (k *KEK) Unwrap(_ context.Context, keyID string, wrapped []byte) ([]byte, error) {
	if k == nil {
		return nil, errors.New("kekstatic: KEK must not be nil")
	}
	k.mu.RLock()
	master := k.keys[keyID]
	k.mu.RUnlock()
	if master == nil {
		return nil, fmt.Errorf("kekstatic: unknown keyID")
	}

	gcm, err := newGCM(master)
	if err != nil {
		return nil, err
	}
	nonceLen := gcm.NonceSize()
	if len(wrapped) < nonceLen {
		return nil, errors.New("kekstatic: wrapped blob too short")
	}
	nonce := wrapped[:nonceLen]
	ct := wrapped[nonceLen:]
	pt, err := gcm.Open(nil, nonce, ct, []byte(keyID))
	if err != nil {
		return nil, fmt.Errorf("kekstatic: gcm open: %w", err)
	}
	return pt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("kekstatic: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("kekstatic: gcm new: %w", err)
	}
	return gcm, nil
}

func validateKeyID(keyID string) error {
	if keyID == "" {
		return errors.New("kekstatic: keyID must not be empty")
	}
	if len(keyID) > 255 {
		return fmt.Errorf("kekstatic: keyID exceeds 255 bytes")
	}
	if !utf8.ValidString(keyID) {
		return errors.New("kekstatic: keyID must be valid UTF-8")
	}
	for _, r := range keyID {
		if r == 0 || unicode.IsControl(r) {
			return errors.New("kekstatic: keyID contains control characters")
		}
		if !isAllowedKeyIDRune(r) {
			return errors.New("kekstatic: keyID must match [A-Za-z0-9._:/-]")
		}
	}
	return nil
}

// isAllowedKeyIDRune permits the alphabet most KMS providers use for
// key identifiers: ASCII letters and digits plus '.', '_', ':', '/',
// and '-'. Anything else (Unicode letters, symbols, spaces) is rejected
// — both to stop a homoglyph from impersonating a configured key in
// telemetry and to keep the envelope header parser well-behaved.
func isAllowedKeyIDRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '.' || r == '_' || r == ':' || r == '/' || r == '-':
		return true
	}
	return false
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
