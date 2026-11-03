package encrypt

import (
	"bytes"
	"errors"
	"fmt"
)

// Key is one entry of a [Keyring]: an ID that is never reused for other
// material (1–32 bytes of [A-Za-z0-9_-]) and a 32-byte AES-256 key.
type Key struct {
	ID  string
	Key []byte
}

// Keyring is an [AEAD] over several AES-256-GCM keys for key rotation: the
// first key seals, every key opens what it sealed. Each ciphertext starts
// with the sealing key's ID and a dot ("id." ‖ iv ‖ ct ‖ tag), so opening
// needs no trial decryption and [Keyring.Current] can tell what a reseal
// still has to move to the newest key.
//
// To rotate: prepend a new key and deploy; reseal stored values that
// [Keyring.Current] reports as not current (Decrypt, then Encrypt); then
// drop the old key. A value sealed with a key no longer listed fails with
// [ErrUnknownKey].
type Keyring struct {
	newest string
	byID   map[string]AEAD
}

// ErrUnknownKey is returned by [Keyring.Decrypt] for a ciphertext sealed
// with a key the ring does not hold.
var ErrUnknownKey = errors.New("encrypt: sealed with a key that is not in the keyring")

// NewKeyring builds the ring from keys, newest first. It returns an error
// for no keys, an invalid or duplicate ID, or a key that is not 32 bytes.
// Key material is copied.
func NewKeyring(keys ...Key) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("encrypt: keyring needs at least one key")
	}
	r := &Keyring{newest: keys[0].ID, byID: make(map[string]AEAD, len(keys))}
	for i, k := range keys {
		if !validKeyID(k.ID) {
			return nil, fmt.Errorf("encrypt: keyring key %d has an invalid ID", i)
		}
		if _, dup := r.byID[k.ID]; dup {
			return nil, fmt.Errorf("encrypt: keyring key ID %q is duplicated", k.ID)
		}
		a, err := NewGCM(k.Key)
		if err != nil {
			return nil, fmt.Errorf("encrypt: keyring key %q: %w", k.ID, err)
		}
		r.byID[k.ID] = a
	}
	return r, nil
}

// Encrypt seals plaintext bound to associatedData with the newest key.
func (r *Keyring) Encrypt(plaintext, associatedData []byte) ([]byte, error) {
	ct, err := r.byID[r.newest].Encrypt(plaintext, associatedData)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(r.newest)+1+len(ct))
	out = append(out, r.newest...)
	out = append(out, '.')
	return append(out, ct...), nil
}

// Decrypt opens a ciphertext any key of the ring sealed.
func (r *Keyring) Decrypt(ciphertext, associatedData []byte) ([]byte, error) {
	id, ct, ok := bytes.Cut(ciphertext, []byte{'.'})
	if !ok || !validKeyID(string(id)) {
		return nil, errors.New("encrypt: ciphertext names no key")
	}
	a, known := r.byID[string(id)]
	if !known {
		return nil, ErrUnknownKey
	}
	return a.Decrypt(ct, associatedData)
}

// Current reports whether the newest key sealed ciphertext.
func (r *Keyring) Current(ciphertext []byte) bool {
	id, _, ok := bytes.Cut(ciphertext, []byte{'.'})
	return ok && string(id) == r.newest
}

func validKeyID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c != '_' && c != '-' && (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}
