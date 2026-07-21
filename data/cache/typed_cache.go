package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// TypedCache wraps a Cache to provide type-safe serialization of T.
// By default values are encoded as JSON via [JSONCodec]; use
// [NewTypedCacheWithCodec] for a custom [Codec].
type TypedCache[T any] struct {
	backend Cache
	prefix  string
	codec   Codec[T]
}

// NewTypedCache creates a TypedCache that serializes T to/from JSON.
// The prefix is prepended verbatim to all keys (full = prefix + key) to
// avoid collisions between caches sharing one backend.
//
// The prefix MUST be empty or end with ':' so related prefixes cannot
// collide (e.g. "user"+"s1" vs "users"+"1"). See [ValidateKeyPrefix].
//
// Returns an error if backend is nil, or if the prefix is invalid.
func NewTypedCache[T any](backend Cache, prefix string) (*TypedCache[T], error) {
	return NewTypedCacheWithCodec(backend, prefix, JSONCodec[T]{})
}

// NewTypedCacheWithCodec is like [NewTypedCache] but uses the supplied
// codec instead of JSON. codec must not be nil.
func NewTypedCacheWithCodec[T any](backend Cache, prefix string, codec Codec[T]) (*TypedCache[T], error) {
	if backend == nil {
		return nil, fmt.Errorf("cache: NewTypedCache requires a non-nil backend")
	}
	if codec == nil {
		return nil, fmt.Errorf("cache: NewTypedCacheWithCodec requires a non-nil codec")
	}
	if err := ValidateKeyPrefix(prefix); err != nil {
		return nil, err
	}
	return &TypedCache[T]{backend: backend, prefix: prefix, codec: codec}, nil
}

// fullKey validates the user-provided key and returns the combined prefix+key.
// Validates both the key itself and the combined length.
func (tc *TypedCache[T]) fullKey(key string) (string, error) {
	if tc == nil || tc.backend == nil {
		return "", ErrInvalidCache
	}
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	full := tc.prefix + key
	if len(full) > MaxKeyLen {
		return "", fmt.Errorf("%w: key with prefix exceeds maximum length", ErrKeyTooLong)
	}
	return full, nil
}

// Get retrieves and deserializes a value. Returns ErrCacheMiss if not found.
func (tc *TypedCache[T]) Get(ctx context.Context, key string) (T, error) {
	var zero T
	full, err := tc.fullKey(key)
	if err != nil {
		return zero, err
	}
	data, err := tc.backend.Get(ctx, full)
	if err != nil {
		return zero, err
	}
	var result T
	if err := tc.codec.Unmarshal(data, &result); err != nil {
		return zero, redact.WrapError("cache unmarshal", err)
	}
	return result, nil
}

// Set serializes and stores a value with the given TTL.
func (tc *TypedCache[T]) Set(ctx context.Context, key string, value T, ttl time.Duration) error {
	full, err := tc.fullKey(key)
	if err != nil {
		return err
	}
	data, err := tc.codec.Marshal(value)
	if err != nil {
		return redact.WrapError("cache marshal", err)
	}
	return tc.backend.Set(ctx, full, data, ttl)
}

// Delete removes a key from the cache.
func (tc *TypedCache[T]) Delete(ctx context.Context, key string) error {
	full, err := tc.fullKey(key)
	if err != nil {
		return err
	}
	return tc.backend.Delete(ctx, full)
}

// Exists checks whether a key exists in the cache.
func (tc *TypedCache[T]) Exists(ctx context.Context, key string) (bool, error) {
	full, err := tc.fullKey(key)
	if err != nil {
		return false, err
	}
	return tc.backend.Exists(ctx, full)
}
