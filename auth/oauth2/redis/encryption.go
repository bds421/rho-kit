package redis

import (
	"context"
	"errors"
	"fmt"

	"github.com/bds421/rho-kit/crypto/v2/envelope"
	"github.com/bds421/rho-kit/crypto/v2/envelope/kekstatic"
)

// sealPurpose is the Box purpose every stored session and login-state
// value is sealed for; the Redis key is its binding.
const sealPurpose = "oauth2/redis/v1"

// maxKeyIDLen bounds EncryptionKey.ID.
const maxKeyIDLen = 32

// sealOverhead is the extra stored size of an encrypted value over its
// plaintext: the envelope header (magic, version, the key ID and the
// wrapped DEK, each length-prefixed) and the body's nonce and tag.
const sealOverhead = 3 + 1 + 2 + maxKeyIDLen + 2 + (12 + 32 + 16) + 12 + 16

// errNotDecryptable reports a stored value that this store cannot decrypt
// because it is unencrypted or sealed with a key that is no longer
// configured. Reads treat it as an absent entry so sessions re-login
// instead of failing.
var errNotDecryptable = errors.New("oauth2/redis: stored value not decryptable with configured keys")

// EncryptionKey is one AES-256 key for [WithEncryption]. ID is stored with
// every value it encrypts so older values still decrypt after rotation.
type EncryptionKey struct {
	// ID names the key: 1-32 bytes of [A-Za-z0-9._:/-]. Never reuse an ID
	// for different key material.
	ID string
	// Key is the 32-byte AES-256 key. It is copied.
	Key []byte
}

type sealer struct {
	box *envelope.Box
}

// WithEncryption seals every stored session and login-state value with
// envelope encryption (an [envelope.Box] over a [kekstatic.KEK] of the
// keys) before it reaches Redis. Recommended for all deployments:
// sessions hold access, refresh and ID tokens. Default: off (values are
// stored as plain JSON).
//
// The first key encrypts; every key decrypts. To rotate, prepend a new key,
// deploy, and remove the old key once entries written with it have expired
// (the session TTL). Each value is bound to its Redis key, so a value
// copied to another key fails authentication.
//
// Reads treat an unencrypted value, or one sealed with a key that is no
// longer configured, as absent (the user logs in again); a value that fails
// authentication returns an error. Panics when no key is given, an ID is
// invalid or duplicated, or a key is not 32 bytes.
func WithEncryption(keys ...EncryptionKey) Option {
	if len(keys) == 0 {
		panic("oauth2/redis: WithEncryption requires at least one key")
	}
	var kek *kekstatic.KEK
	for i, k := range keys {
		var err error
		switch {
		case len(k.ID) > maxKeyIDLen:
			err = errors.New("kekstatic: keyID exceeds 32 bytes")
		case i == 0:
			kek, err = kekstatic.NewKEK(k.ID, k.Key)
		default:
			err = kek.AddKey(k.ID, k.Key)
		}
		if err != nil {
			panic(fmt.Sprintf("oauth2/redis: WithEncryption: %v", err))
		}
	}
	s := &sealer{box: envelope.NewBox(kek, sealPurpose)}
	return func(st *Store) { st.sealer = s }
}

// seal and open run the static KEK, which needs no context.
func (s *sealer) seal(redisKey string, plaintext []byte) ([]byte, error) {
	sealed, err := s.box.Seal(context.Background(), plaintext, redisKey)
	if err != nil {
		return nil, errors.New("oauth2/redis: encrypt value")
	}
	return sealed, nil
}

func (s *sealer) open(redisKey string, stored []byte) ([]byte, error) {
	plaintext, err := s.box.Open(context.Background(), stored, redisKey)
	switch {
	case errors.Is(err, envelope.ErrMalformed), errors.Is(err, envelope.ErrTruncated),
		errors.Is(err, envelope.ErrUnsupportedVer), errors.Is(err, kekstatic.ErrUnknownKeyID):
		return nil, errNotDecryptable
	case err != nil:
		return nil, errors.New("oauth2/redis: stored value is invalid or failed authentication")
	}
	return plaintext, nil
}
