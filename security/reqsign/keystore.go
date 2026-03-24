package reqsign

// minKeyLen is the minimum key length for HMAC-SHA256 (matches hash output size).
const minKeyLen = 32

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

// NewStaticKeyStore creates a StaticKeyStore with the given keys and current key ID.
//
// Panics if currentID is not present in keys, if keys is empty, or if any key
// is shorter than 32 bytes. This follows the fail-fast convention: configuration
// errors surface at startup, not at request time.
func NewStaticKeyStore(keys map[string][]byte, currentID string) *StaticKeyStore {
	if len(keys) == 0 {
		panic("reqsign: keys map must not be empty")
	}
	if _, ok := keys[currentID]; !ok {
		panic("reqsign: currentID " + currentID + " not found in keys map")
	}
	for id, k := range keys {
		if len(k) < minKeyLen {
			panic("reqsign: key " + id + " must be at least 32 bytes")
		}
	}

	// Defensive copy to prevent mutation of the caller's map.
	copied := make(map[string][]byte, len(keys))
	for id, k := range keys {
		dst := make([]byte, len(k))
		copy(dst, k)
		copied[id] = dst
	}

	return &StaticKeyStore{
		keys:      copied,
		currentID: currentID,
	}
}

// Key returns the secret for the given key ID. Returns nil, false if not found.
// The returned slice is a defensive copy; callers cannot mutate internal state.
func (s *StaticKeyStore) Key(keyID string) ([]byte, bool) {
	k, ok := s.keys[keyID]
	if !ok {
		return nil, false
	}
	dst := make([]byte, len(k))
	copy(dst, k)
	return dst, true
}

// keyUnsafe returns the internal key slice without copying. This avoids
// allocation on the hot path for internal sign/verify operations where the
// caller does not expose the slice. The returned slice MUST NOT be mutated
// or retained beyond the calling function's scope.
func (s *StaticKeyStore) keyUnsafe(keyID string) ([]byte, bool) {
	k, ok := s.keys[keyID]
	return k, ok
}

// currentKeyUnsafe returns the active signing key ID and internal secret
// slice without copying. Same safety constraints as keyUnsafe apply.
func (s *StaticKeyStore) currentKeyUnsafe() (string, []byte) {
	return s.currentID, s.keys[s.currentID]
}

// CurrentKeyID returns the active signing key ID and secret.
// The returned slice is a defensive copy; callers cannot mutate internal state.
func (s *StaticKeyStore) CurrentKeyID() (string, []byte) {
	k := s.keys[s.currentID]
	dst := make([]byte, len(k))
	copy(dst, k)
	return s.currentID, dst
}
