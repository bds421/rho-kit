package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// TypedCache wraps a Cache to provide type-safe JSON serialization.
// It marshals/unmarshals values of type T transparently.
type TypedCache[T any] struct {
	backend Cache
	prefix  string
}

// NewTypedCache creates a TypedCache that serializes T to/from JSON.
// The prefix is prepended to all keys to avoid collisions.
//
// Returns an error if backend is nil, or if the prefix contains invalid
// characters or is too long. The combined prefix+key must fit within
// MaxKeyLen (checked per-operation in fullKey). A prefix longer than
// MaxKeyPrefixLen is rejected upfront to guarantee at least MaxKeyPrefixLen
// bytes remain for keys.
func NewTypedCache[T any](backend Cache, prefix string) (*TypedCache[T], error) {
	if backend == nil {
		return nil, fmt.Errorf("cache: NewTypedCache requires a non-nil backend")
	}
	if err := ValidateKeyPrefix(prefix); err != nil {
		return nil, err
	}
	return &TypedCache[T]{backend: backend, prefix: prefix}, nil
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
	if err := json.Unmarshal(data, &result); err != nil {
		return zero, fmt.Errorf("cache unmarshal: %w", err)
	}
	return result, nil
}

// Set serializes and stores a value with the given TTL.
func (tc *TypedCache[T]) Set(ctx context.Context, key string, value T, ttl time.Duration) error {
	full, err := tc.fullKey(key)
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache marshal: %w", err)
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
