package signing

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// minKeyLen is the minimum key length for HMAC-SHA256 (matches hash output size).
const minKeyLen = 32

// maxKeyIDLen caps key IDs before they become HTTP header values or
// secret-store lookup keys.
const maxKeyIDLen = 256

// UnsafeKeyStore provides zero-copy key access for performance-critical paths.
// Implementations must guarantee the returned slice is never mutated.
type UnsafeKeyStore interface {
	KeyUnsafe(keyID string) ([]byte, bool)
	CurrentKeyUnsafe() (string, []byte)
}

// KeyStore manages signing keys. Implementations must be safe for concurrent use.
//
// WARNING: The canonical string does not include the Host header. If keys are
// shared across services, signatures are portable between them — a valid
// signature for service A can be replayed against service B at the same path.
// Use unique per-service-pair keys to prevent cross-service replay.
type KeyStore interface {
	// Key returns the secret for the given key ID. Returns nil, false if not found.
	Key(keyID string) ([]byte, bool)
	// CurrentKeyID returns the active signing key ID and secret.
	CurrentKeyID() (string, []byte)
}

// NilKeyStoreMsg is the panic message used when a nil KeyStore is passed.
const NilKeyStoreMsg = "signing: KeyStore must not be nil"

// StaticKeyStore holds a fixed set of keys. Multiple keys support rotation:
// sign with current, verify against any.
type StaticKeyStore struct {
	// keys is a defensively copied map set once in NewStaticKeyStore and never
	// modified afterward. Read-only access from Key and CurrentKeyID is safe
	// for concurrent use without a mutex.
	keys map[string][]byte
	// currentID is set once in NewStaticKeyStore and never modified afterward.
	currentID string
}

// NewStaticKeyStoreE creates a StaticKeyStore with the given keys and
// current key ID, returning a descriptive error on misconfiguration. Use
// this variant when keys come from runtime sources (env, KMS, config
// reload) where one bad rotation should not crash the process.
//
// Returns errors for: empty keys map, currentID not present, any key
// shorter than [minKeyLen]. The store keeps a defensive copy of the keys
// map so callers may mutate or zero their copy after construction.
func NewStaticKeyStoreE(keys map[string][]byte, currentID string) (*StaticKeyStore, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("signing: keys map must not be empty")
	}
	if err := validateKeyID(currentID); err != nil {
		return nil, fmt.Errorf("signing: currentID: %w", err)
	}
	if _, ok := keys[currentID]; !ok {
		return nil, fmt.Errorf("signing: currentID not found in keys map")
	}
	for id, k := range keys {
		if err := validateKeyID(id); err != nil {
			return nil, fmt.Errorf("signing: key ID is invalid: %w", err)
		}
		if len(k) < minKeyLen {
			return nil, fmt.Errorf("signing: key must meet minimum length")
		}
	}

	copied := make(map[string][]byte, len(keys))
	for id, k := range keys {
		dst := make([]byte, len(k))
		copy(dst, k)
		copied[id] = dst
	}

	return &StaticKeyStore{
		keys:      copied,
		currentID: currentID,
	}, nil
}

// NewStaticKeyStore is the panic-on-error variant of [NewStaticKeyStoreE].
// Prefer the error-returning version for runtime config; reserve this one
// for tests and contexts where the keys are compile-time constants.
//
// Panics if currentID is not present in keys, if keys is empty, or if any
// key is shorter than [minKeyLen].
func NewStaticKeyStore(keys map[string][]byte, currentID string) *StaticKeyStore {
	s, err := NewStaticKeyStoreE(keys, currentID)
	if err != nil {
		panic("signing: static key store configuration is invalid")
	}
	return s
}

// Key returns the secret for the given key ID. Returns nil, false if not found.
// The returned slice is a defensive copy; callers cannot mutate internal state.
func (s *StaticKeyStore) Key(keyID string) ([]byte, bool) {
	if s == nil || s.keys == nil {
		return nil, false
	}
	k, ok := s.keys[keyID]
	if !ok {
		return nil, false
	}
	dst := make([]byte, len(k))
	copy(dst, k)
	return dst, true
}

// KeyUnsafe returns the internal key slice without copying. This avoids
// allocation on the hot path for internal sign/verify operations where the
// caller does not expose the slice. The returned slice MUST NOT be mutated
// or retained beyond the calling function's scope.
func (s *StaticKeyStore) KeyUnsafe(keyID string) ([]byte, bool) {
	if s == nil || s.keys == nil {
		return nil, false
	}
	k, ok := s.keys[keyID]
	return k, ok
}

// CurrentKeyUnsafe returns the active signing key ID and internal secret
// slice without copying. Same safety constraints as KeyUnsafe apply.
func (s *StaticKeyStore) CurrentKeyUnsafe() (string, []byte) {
	if s == nil || s.keys == nil {
		return "", nil
	}
	return s.currentID, s.keys[s.currentID]
}

// CurrentKeyID returns the active signing key ID and secret.
// The returned slice is a defensive copy; callers cannot mutate internal state.
func (s *StaticKeyStore) CurrentKeyID() (string, []byte) {
	if s == nil || s.keys == nil {
		return "", nil
	}
	k := s.keys[s.currentID]
	dst := make([]byte, len(k))
	copy(dst, k)
	return s.currentID, dst
}

func validateKeyID(keyID string) error {
	if keyID == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(keyID) > maxKeyIDLen {
		return fmt.Errorf("exceeds maximum length")
	}
	if !utf8.ValidString(keyID) {
		return fmt.Errorf("must be valid UTF-8")
	}
	if strings.Contains(keyID, ",") {
		return fmt.Errorf("must not contain commas")
	}
	for _, r := range keyID {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("must not contain control or whitespace characters")
		}
	}
	return nil
}
