package signing

import (
	"fmt"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/secret"
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
//
// Each key is wrapped in [secret.String] so the raw bytes can be zeroed at
// shutdown via [StaticKeyStore.Close]. Memory dumps (core file,
// /proc/<pid>/mem on Linux, swap inspection) recover only zeroes after a
// successful Close.
type StaticKeyStore struct {
	// keys is a defensively copied map set once in NewStaticKeyStore and never
	// modified afterward (the map shape; the wrapped secrets themselves can
	// be zeroed via Close). Read-only access from Key and CurrentKeyID is
	// safe for concurrent use without a mutex.
	keys map[string]*secret.String
	// currentID is set once in NewStaticKeyStore and never modified afterward.
	currentID string
	closed    atomic.Bool
}

// NewStaticKeyStore creates a StaticKeyStore with the given keys and
// current key ID, returning a descriptive error on misconfiguration. Use
// this for runtime-sourced keys (env, KMS, config reload) where one
// bad rotation should not crash the process.
//
// Returns errors for: empty keys map, currentID not present, any key
// shorter than [minKeyLen]. The store keeps a defensive copy of the keys
// map so callers may mutate or zero their copy after construction.
//
// For compile-time-known keys, see [MustNewStaticKeyStore].
func NewStaticKeyStore(keys map[string][]byte, currentID string) (*StaticKeyStore, error) {
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

	copied := make(map[string]*secret.String, len(keys))
	for id, k := range keys {
		copied[id] = secret.New(k)
	}

	return &StaticKeyStore{
		keys:      copied,
		currentID: currentID,
	}, nil
}

// MustNewStaticKeyStore is the panic-on-error variant of
// [NewStaticKeyStore]. Use it only when keys are compile-time
// constants — panics force a process crash, which is the right
// behaviour at startup with hard-coded keys and the wrong behaviour
// for runtime-loaded config.
//
// Panics if currentID is not present in keys, if keys is empty, or if
// any key is shorter than [minKeyLen].
func MustNewStaticKeyStore(keys map[string][]byte, currentID string) *StaticKeyStore {
	s, err := NewStaticKeyStore(keys, currentID)
	if err != nil {
		panic("signing: static key store configuration is invalid")
	}
	return s
}

// Key returns the secret for the given key ID. Returns nil, false if not found.
// The returned slice is a defensive copy; callers cannot mutate internal state.
//
// Returns nil, false after [StaticKeyStore.Close] has zeroed the wrapped
// secrets — callers downstream must treat that as the key store being
// shut down.
func (s *StaticKeyStore) Key(keyID string) ([]byte, bool) {
	if s == nil || s.keys == nil || s.closed.Load() {
		return nil, false
	}
	k, ok := s.keys[keyID]
	if !ok || k == nil || k.IsEmpty() {
		return nil, false
	}
	return k.Reveal(), true
}

// KeyUnsafe is retained for API compatibility with v1 callers but now
// returns a defensive copy — the wrapped [secret.String] does not expose
// a non-copying view. The historic "MUST NOT be mutated or retained"
// contract still applies.
func (s *StaticKeyStore) KeyUnsafe(keyID string) ([]byte, bool) {
	return s.Key(keyID)
}

// CurrentKeyUnsafe returns the active signing key ID and a defensive copy
// of the key bytes. See [StaticKeyStore.KeyUnsafe] for the historical
// "Unsafe" contract.
func (s *StaticKeyStore) CurrentKeyUnsafe() (string, []byte) {
	if s == nil || s.keys == nil || s.closed.Load() {
		return "", nil
	}
	k, ok := s.keys[s.currentID]
	if !ok || k == nil || k.IsEmpty() {
		return s.currentID, nil
	}
	return s.currentID, k.Reveal()
}

// CurrentKeyID returns the active signing key ID and secret.
// The returned slice is a defensive copy; callers cannot mutate internal state.
func (s *StaticKeyStore) CurrentKeyID() (string, []byte) {
	if s == nil || s.keys == nil || s.closed.Load() {
		return "", nil
	}
	k, ok := s.keys[s.currentID]
	if !ok || k == nil || k.IsEmpty() {
		return s.currentID, nil
	}
	return s.currentID, k.Reveal()
}

// Close zeroes every wrapped key in the store. Subsequent Key /
// CurrentKeyID / KeyUnsafe / CurrentKeyUnsafe calls return empty
// values. Idempotent — calling Close on an already-closed store is
// a no-op.
//
// Close is intended for graceful shutdown paths where the kit owns
// the key material's lifecycle (typically alongside server.Close()).
func (s *StaticKeyStore) Close() error {
	if s == nil {
		return nil
	}
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	for _, k := range s.keys {
		if k != nil {
			k.Zero()
		}
	}
	return nil
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
