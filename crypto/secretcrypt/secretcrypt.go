// Package secretcrypt provides reversible AES-256-GCM encryption with
// HKDF-derived keys per domain label and identity. Use for secrets that
// must be read back (webhook signing keys, OAuth tokens) rather than
// one-way password hashes ([github.com/bds421/rho-kit/crypto/v2/passhash]).
package secretcrypt

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"

	"github.com/bds421/rho-kit/crypto/v2/encrypt"
)

var (
	// ErrEmptyMaster is returned when New receives an empty master key.
	ErrEmptyMaster = errors.New("secretcrypt: master key must not be empty")
	// ErrShortMaster is returned when the master key is shorter than minMasterLen.
	ErrShortMaster = errors.New("secretcrypt: master key must be at least 32 bytes")
	// ErrEmptyDomainLabel is returned when New receives an empty domain label.
	ErrEmptyDomainLabel = errors.New("secretcrypt: domain label must not be empty")
	// ErrEmptyIdentity is returned when Encrypt/Decrypt receive an empty identity.
	ErrEmptyIdentity = errors.New("secretcrypt: identity must not be empty")
)

const (
	derivedKeyLen = 32
	minMasterLen  = 32
)

// Crypter encrypts and decrypts secrets with per-identity keys derived
// from a single master via HKDF-SHA256.
type Crypter struct {
	master []byte
	label  string
}

// New constructs a Crypter. master is copied; domainLabel scopes derived
// keys so the same identity under different labels yields unrelated ciphertext.
func New(master []byte, domainLabel string) (*Crypter, error) {
	if len(master) == 0 {
		return nil, ErrEmptyMaster
	}
	if len(master) < minMasterLen {
		return nil, ErrShortMaster
	}
	if domainLabel == "" {
		return nil, ErrEmptyDomainLabel
	}
	cp := make([]byte, len(master))
	copy(cp, master)
	return &Crypter{master: cp, label: domainLabel}, nil
}

// Encrypt seals plaintext with AES-256-GCM. identity participates in key
// derivation; aad is authenticated but not encrypted (e.g. tenant id).
func (c *Crypter) Encrypt(identity string, plaintext, aad []byte) ([]byte, error) {
	if identity == "" {
		return nil, ErrEmptyIdentity
	}
	aead, err := c.aead(identity)
	if err != nil {
		return nil, err
	}
	return encrypt.EncryptBytesAAD(aead, plaintext, aad)
}

// Decrypt opens a blob produced by [Crypter.Encrypt] for the same identity
// and aad.
func (c *Crypter) Decrypt(identity string, blob, aad []byte) ([]byte, error) {
	if identity == "" {
		return nil, ErrEmptyIdentity
	}
	aead, err := c.aead(identity)
	if err != nil {
		return nil, err
	}
	return encrypt.DecryptBytesAAD(aead, blob, aad)
}

func (c *Crypter) aead(identity string) (encrypt.AEAD, error) {
	key, err := deriveKey(c.master, c.label, identity)
	if err != nil {
		return nil, fmt.Errorf("secretcrypt: derive key: %w", err)
	}
	return encrypt.NewGCM(key)
}

func deriveKey(master []byte, label, identity string) ([]byte, error) {
	r := hkdf.New(sha256.New, master, []byte(identity), []byte(label))
	key := make([]byte, derivedKeyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}
