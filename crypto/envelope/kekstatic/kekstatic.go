// Package kekstatic provides an in-memory [envelope.KEK] implementation
// suitable for tests and development. Each named key wraps DEKs with
// AES-256-GCM. Production deployments must use a cloud KMS subpackage
// (kekaws/kekgcp/kekvault — TODO).
//
// Static KEKs are NOT a substitute for a KMS:
//
//   - The master key lives in process memory; a heap dump leaks every
//     ciphertext written under it.
//   - There is no audit trail.
//   - There is no enforced rotation cadence.
//
// Use kekstatic for unit tests, integration tests with deterministic
// fixtures, and local-dev workflows. For production envelopes, write a
// thin KEK adapter against your provider's SDK.
package kekstatic

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
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

// New returns a KEK with the given keyID active and no other keys
// available. masterKey must be 32 bytes (AES-256).
func New(keyID string, masterKey []byte) (*KEK, error) {
	if keyID == "" {
		return nil, errors.New("kekstatic: keyID must not be empty")
	}
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("kekstatic: masterKey must be 32 bytes, got %d", len(masterKey))
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
	if keyID == "" {
		return errors.New("kekstatic: keyID must not be empty")
	}
	if len(masterKey) != 32 {
		return fmt.Errorf("kekstatic: masterKey must be 32 bytes, got %d", len(masterKey))
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, exists := k.keys[keyID]; exists {
		return fmt.Errorf("kekstatic: keyID %q already registered", keyID)
	}
	k.keys[keyID] = append([]byte(nil), masterKey...)
	return nil
}

// Rotate makes keyID the active key. The keyID must already be
// registered via [New] or [KEK.AddKey]. Subsequent Wrap calls embed
// the new keyID; existing blobs continue to decrypt via the older key
// until they are rewrapped.
func (k *KEK) Rotate(keyID string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, exists := k.keys[keyID]; !exists {
		return fmt.Errorf("kekstatic: keyID %q not registered", keyID)
	}
	k.current = keyID
	return nil
}

// RemoveKey deletes the named key. Use only after every blob written
// under it has been rewrapped. RemoveKey panics if asked to remove the
// active key — that is unrecoverable.
func (k *KEK) RemoveKey(keyID string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.current == keyID {
		panic("kekstatic: cannot remove the active keyID; rotate first")
	}
	delete(k.keys, keyID)
}

// KeyID returns the active key identifier.
func (k *KEK) KeyID() string {
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
	k.mu.RLock()
	keyID := k.current
	master := k.keys[keyID]
	k.mu.RUnlock()
	if master == nil {
		return "", nil, errors.New("kekstatic: no active key")
	}

	gcm, err := newGCM(master)
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
	k.mu.RLock()
	master := k.keys[keyID]
	k.mu.RUnlock()
	if master == nil {
		return nil, fmt.Errorf("kekstatic: unknown keyID %q", keyID)
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
