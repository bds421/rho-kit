package redis

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/bds421/rho-kit/crypto/v2/encrypt"
)

// sealedPrefix marks an encrypted value: "rk1." + key ID + "." + nonce ‖
// ciphertext ‖ tag. Plain JSON values start with '{' and never match.
const sealedPrefix = "rk1."

// maxKeyIDLen bounds EncryptionKey.ID.
const maxKeyIDLen = 32

// sealOverhead is the extra stored size of an encrypted value over its
// plaintext (prefix, key ID, separator, 12-byte nonce, 16-byte tag).
const sealOverhead = len(sealedPrefix) + maxKeyIDLen + 1 + 12 + 16

// errNotDecryptable reports a stored value that this store cannot decrypt
// because it is unencrypted or sealed with a key that is no longer
// configured. Reads treat it as an absent entry so sessions re-login
// instead of failing.
var errNotDecryptable = errors.New("oauth2/redis: stored value not decryptable with configured keys")

// EncryptionKey is one AES-256-GCM key for [WithEncryption]. ID is stored
// with every value it encrypts so older values still decrypt after rotation.
type EncryptionKey struct {
	// ID names the key: 1-32 bytes of [A-Za-z0-9_-]. Never reuse an ID for
	// different key material.
	ID string
	// Key is the 32-byte AES-256 key. It is copied.
	Key []byte
}

type sealer struct {
	activeID string
	active   encrypt.AEAD
	byID     map[string]encrypt.AEAD
}

// WithEncryption encrypts every stored session and login-state value with
// AES-256-GCM (via crypto/encrypt) before it reaches Redis. Recommended for
// all deployments: sessions hold access, refresh and ID tokens. Default: off
// (values are stored as plain JSON).
//
// The first key encrypts; every key decrypts. To rotate, prepend a new key,
// deploy, and remove the old key once entries written with it have expired
// (the session TTL). Each value is bound to its Redis key as associated
// data, so a value copied to another key fails authentication.
//
// Reads treat an unencrypted value, or one sealed with a key that is no
// longer configured, as absent (the user logs in again); a value that fails
// authentication returns an error. Panics when no key is given, an ID is
// invalid or duplicated, or a key is not 32 bytes.
func WithEncryption(keys ...EncryptionKey) Option {
	if len(keys) == 0 {
		panic("oauth2/redis: WithEncryption requires at least one key")
	}
	s := &sealer{byID: make(map[string]encrypt.AEAD, len(keys))}
	for i, k := range keys {
		if !validKeyID(k.ID) {
			panic(fmt.Sprintf("oauth2/redis: WithEncryption key %d has an invalid ID", i))
		}
		if _, dup := s.byID[k.ID]; dup {
			panic(fmt.Sprintf("oauth2/redis: WithEncryption key ID %q is duplicated", k.ID))
		}
		aead, err := encrypt.NewGCM(k.Key)
		if err != nil {
			panic(fmt.Sprintf("oauth2/redis: WithEncryption key %q must be 32 bytes", k.ID))
		}
		s.byID[k.ID] = aead
		if i == 0 {
			s.activeID, s.active = k.ID, aead
		}
	}
	return func(st *Store) { st.sealer = s }
}

func (s *sealer) seal(redisKey string, plaintext []byte) ([]byte, error) {
	ct, err := encrypt.EncryptBytesAAD(s.active, plaintext, []byte(redisKey))
	if err != nil {
		return nil, errors.New("oauth2/redis: encrypt value")
	}
	out := make([]byte, 0, len(sealedPrefix)+len(s.activeID)+1+len(ct))
	out = append(out, sealedPrefix...)
	out = append(out, s.activeID...)
	out = append(out, '.')
	return append(out, ct...), nil
}

func (s *sealer) open(redisKey string, stored []byte) ([]byte, error) {
	rest, ok := bytes.CutPrefix(stored, []byte(sealedPrefix))
	if !ok {
		return nil, errNotDecryptable
	}
	id, ct, ok := bytes.Cut(rest, []byte{'.'})
	if !ok {
		return nil, errors.New("oauth2/redis: stored value is invalid")
	}
	aead, ok := s.byID[string(id)]
	if !ok {
		return nil, errNotDecryptable
	}
	plaintext, err := encrypt.DecryptBytesAAD(aead, ct, []byte(redisKey))
	if err != nil {
		return nil, errors.New("oauth2/redis: stored value failed authentication")
	}
	return plaintext, nil
}

func validKeyID(id string) bool {
	if id == "" || len(id) > maxKeyIDLen {
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
