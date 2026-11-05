package redis

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/bds421/rho-kit/crypto/v2/encrypt"
)

// sealedPrefix marks an encrypted value: "rk1." followed by the
// [encrypt.Keyring] ciphertext (key ID + "." + nonce ‖ ciphertext ‖ tag).
// Plain JSON values start with '{' and never match.
const sealedPrefix = "rk1."

// maxKeyIDLen bounds EncryptionKey.ID, as [encrypt.Key] does.
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
	ring *encrypt.Keyring
}

// WithEncryption encrypts every stored session and login-state value with
// AES-256-GCM (an [encrypt.Keyring]) before it reaches Redis. Recommended
// for all deployments: sessions hold access, refresh and ID tokens.
// Default: off (values are stored as plain JSON).
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
	ringKeys := make([]encrypt.Key, len(keys))
	for i, k := range keys {
		ringKeys[i] = encrypt.Key{ID: k.ID, Key: k.Key}
	}
	ring, err := encrypt.NewKeyring(ringKeys...)
	if err != nil {
		panic(fmt.Sprintf("oauth2/redis: WithEncryption: %v", err))
	}
	s := &sealer{ring: ring}
	return func(st *Store) { st.sealer = s }
}

func (s *sealer) seal(redisKey string, plaintext []byte) ([]byte, error) {
	ct, err := s.ring.Encrypt(plaintext, []byte(redisKey))
	if err != nil {
		return nil, errors.New("oauth2/redis: encrypt value")
	}
	out := make([]byte, 0, len(sealedPrefix)+len(ct))
	out = append(out, sealedPrefix...)
	return append(out, ct...), nil
}

func (s *sealer) open(redisKey string, stored []byte) ([]byte, error) {
	ct, ok := bytes.CutPrefix(stored, []byte(sealedPrefix))
	if !ok {
		return nil, errNotDecryptable
	}
	plaintext, err := s.ring.Decrypt(ct, []byte(redisKey))
	if errors.Is(err, encrypt.ErrUnknownKey) {
		return nil, errNotDecryptable
	}
	if err != nil {
		return nil, errors.New("oauth2/redis: stored value is invalid or failed authentication")
	}
	return plaintext, nil
}
