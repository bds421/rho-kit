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
