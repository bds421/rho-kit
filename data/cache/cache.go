package cache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrCacheMiss is returned when a key is not found in the cache.
var ErrCacheMiss = errors.New("cache: key not found")

// ErrAdmissionRejected is returned when a backend silently dropped an
// otherwise-valid write. Ristretto's TinyLFU admission policy can reject
// entries that are believed less valuable than what is already cached;
// surfacing the rejection lets callers decide whether to retry, fall back,
// or treat the write as critical.
var ErrAdmissionRejected = errors.New("cache: write rejected by admission policy")

// ErrKeyEmpty is returned when a cache key is empty.
var ErrKeyEmpty = errors.New("cache: key must not be empty")

// ErrKeyTooLong is returned when a cache key exceeds MaxKeyLen bytes.
var ErrKeyTooLong = errors.New("cache: key exceeds maximum length")

// ErrKeyInvalidChars is returned when a cache key contains null bytes,
// newlines, or carriage returns.
var ErrKeyInvalidChars = errors.New("cache: key contains invalid characters")

// MaxKeyLen is the maximum allowed length for cache keys.
const MaxKeyLen = 1024

// ValidateKey checks that a cache key is safe for use. This prevents:
//   - Empty keys: always a programming error
//   - Null bytes: can truncate C strings in some backends
//   - Newlines/carriage returns: can break protocol framing
//   - Excessively long keys: waste memory and indicate dynamic data
//
// All Cache implementations should call this in their public methods to
// ensure consistent validation behavior between test (MemoryCache) and
// production (RedisCache) environments.
func ValidateKey(key string) error {
	if key == "" {
		return ErrKeyEmpty
	}
	if len(key) > MaxKeyLen {
		return fmt.Errorf("%w (%d bytes, max %d)", ErrKeyTooLong, len(key), MaxKeyLen)
	}
	if strings.ContainsAny(key, "\x00\n\r") {
		return ErrKeyInvalidChars
	}
	return nil
}

// Cache defines a generic, backend-agnostic caching interface.
// Implementations must be safe for concurrent use.
type Cache interface {
	// Get retrieves a value by key. Returns ErrCacheMiss if the key does
	// not exist or has expired.
	Get(ctx context.Context, key string) ([]byte, error)

	// Set stores a value with an expiration duration. A zero TTL means
	// the entry does not expire (use sparingly).
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// Delete removes a key. Returns nil if the key does not exist.
	Delete(ctx context.Context, key string) error

	// Exists checks whether a key exists without retrieving its value.
	Exists(ctx context.Context, key string) (bool, error)
}

// BulkCache extends [Cache] with batch-friendly and CAS operations.
// Backends that can implement these efficiently (Redis MGET/MSET/SET NX,
// in-memory single-lock fan-out) should satisfy this interface; others
// can use the [MGet], [MSet], [SetNX] free functions, which fall back to
// individual Cache method calls.
//
// SetNX is the "set-if-not-exists" primitive needed for cross-process
// compute-once at the cache layer (without it, two replicas can compute
// the same expensive value in parallel even though one of them just
// pushed it).
type BulkCache interface {
	Cache

	// MGet retrieves values for multiple keys. The returned map only
	// includes keys that were present; missing keys are silently absent.
	// Errors are returned as-is from the backend.
	MGet(ctx context.Context, keys []string) (map[string][]byte, error)

	// MSet stores all items with a shared ttl. MSet is NOT guaranteed to
	// be all-or-nothing: the Redis backend uses a pipeline (a connection
	// failure mid-batch can leave a partial write), and the in-memory
	// backend fans out per-key Set. Callers that need transactional
	// semantics across multiple keys must build that on top with a
	// MULTI/EXEC or Lua-script path of their own.
	MSet(ctx context.Context, items map[string][]byte, ttl time.Duration) error

	// SetNX stores value only if key does not exist. Returns true when
	// the value was stored, false when the key already had a value.
	SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
}

// MGet returns a map of values for keys, falling back to per-key Get when
// the backend does not implement [BulkCache]. Missing keys are silently
// absent from the result.
func MGet(ctx context.Context, c Cache, keys []string) (map[string][]byte, error) {
	if bc, ok := c.(BulkCache); ok {
		return bc.MGet(ctx, keys)
	}
	out := make(map[string][]byte, len(keys))
	for _, k := range keys {
		v, err := c.Get(ctx, k)
		if err != nil {
			if errors.Is(err, ErrCacheMiss) {
				continue
			}
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// MSet stores multiple items, falling back to per-key Set when the backend
// does not implement [BulkCache]. Stops at the first error.
func MSet(ctx context.Context, c Cache, items map[string][]byte, ttl time.Duration) error {
	if bc, ok := c.(BulkCache); ok {
		return bc.MSet(ctx, items, ttl)
	}
	for k, v := range items {
		if err := c.Set(ctx, k, v, ttl); err != nil {
			return err
		}
	}
	return nil
}

// SetNX stores value only if key does not exist. Falls back to a racy
// Exists+Set sequence when the backend does not implement [BulkCache] —
// note that the fallback is NOT atomic across replicas; only the
// BulkCache-native implementation provides cross-process compute-once.
func SetNX(ctx context.Context, c Cache, key string, value []byte, ttl time.Duration) (bool, error) {
	if bc, ok := c.(BulkCache); ok {
		return bc.SetNX(ctx, key, value, ttl)
	}
	exists, err := c.Exists(ctx, key)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if err := c.Set(ctx, key, value, ttl); err != nil {
		return false, err
	}
	return true, nil
}
