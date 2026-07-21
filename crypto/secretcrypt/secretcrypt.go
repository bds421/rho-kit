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
	"sync"
	"sync/atomic"

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
	// ErrInvalidCrypter is returned when Encrypt/Decrypt is called on a nil
	// or zero-value Crypter (one not constructed via [New]).
	ErrInvalidCrypter = errors.New("secretcrypt: Crypter must be constructed with New")
	// ErrClosed is returned when Encrypt/Decrypt is called after [Crypter.Close].
	ErrClosed = errors.New("secretcrypt: Crypter is closed")
)

const (
	derivedKeyLen = 32
	minMasterLen  = 32
	// maxAEADCache is the per-Crypter identity→AEAD cache cap. Bounded so a
	// high-cardinality identity stream cannot grow the cache without limit;
	// on overflow one arbitrary entry is dropped (map iteration order).
	maxAEADCache = 256
)

// Crypter encrypts and decrypts secrets with per-identity keys derived
// from a single master via HKDF-SHA256.
//
// Derived AEADs are cached per identity so repeated Encrypt/Decrypt for
// the same identity does not re-run HKDF or rebuild the AES-GCM primitive.
// The cache is cleared on [Crypter.Close].
type Crypter struct {
	master []byte
	label  string
	closed atomic.Bool

	mu   sync.RWMutex
	aead map[string]encrypt.AEAD
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
	return &Crypter{master: cp, label: domainLabel, aead: make(map[string]encrypt.AEAD)}, nil
}

// Encrypt seals plaintext with AES-256-GCM. identity participates in key
// derivation; aad is authenticated but not encrypted (e.g. tenant id).
func (c *Crypter) Encrypt(identity string, plaintext, aad []byte) ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if identity == "" {
		return nil, ErrEmptyIdentity
	}
	aead, err := c.aeadFor(identity)
	if err != nil {
		return nil, err
	}
	return encrypt.EncryptBytesAAD(aead, plaintext, aad)
}

// Decrypt opens a blob produced by [Crypter.Encrypt] for the same identity
// and aad.
func (c *Crypter) Decrypt(identity string, blob, aad []byte) ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if identity == "" {
		return nil, ErrEmptyIdentity
	}
	aead, err := c.aeadFor(identity)
	if err != nil {
		return nil, err
	}
	return encrypt.DecryptBytesAAD(aead, blob, aad)
}

func (c *Crypter) validate() error {
	if c == nil || len(c.master) < minMasterLen || c.label == "" {
		return ErrInvalidCrypter
	}
	if c.closed.Load() {
		return ErrClosed
	}
	return nil
}

// Close zeroes the master key and drops the AEAD cache. Subsequent
// Encrypt/Decrypt calls return [ErrClosed]. Idempotent.
func (c *Crypter) Close() error {
	if c == nil {
		return nil
	}
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	c.mu.Lock()
	for i := range c.master {
		c.master[i] = 0
	}
	c.aead = nil
	c.mu.Unlock()
	return nil
}

func (c *Crypter) aeadFor(identity string) (encrypt.AEAD, error) {
	c.mu.RLock()
	if c.closed.Load() {
		c.mu.RUnlock()
		return nil, ErrClosed
	}
	if a, ok := c.aead[identity]; ok {
		c.mu.RUnlock()
		return a, nil
	}
	// Snapshot master under the read lock so Close cannot zero it mid-derive.
	master := append([]byte(nil), c.master...)
	label := c.label
	c.mu.RUnlock()

	key, err := deriveKey(master, label, identity)
	for i := range master {
		master[i] = 0
	}
	if err != nil {
		return nil, fmt.Errorf("secretcrypt: derive key: %w", err)
	}
	// NewGCM copies key material; zero the local HKDF-derived slice so
	// it does not linger in the heap until GC.
	aead, err := encrypt.NewGCM(key)
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return nil, ErrClosed
	}
	if c.aead == nil {
		c.aead = make(map[string]encrypt.AEAD)
	}
	if existing, ok := c.aead[identity]; ok {
		return existing, nil
	}
	if len(c.aead) >= maxAEADCache {
		// Drop one arbitrary entry to bound memory under identity churn.
		for k := range c.aead {
			delete(c.aead, k)
			break
		}
	}
	c.aead[identity] = aead
	return aead, nil
}

func deriveKey(master []byte, label, identity string) ([]byte, error) {
	r := hkdf.New(sha256.New, master, []byte(identity), []byte(label))
	key := make([]byte, derivedKeyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}
