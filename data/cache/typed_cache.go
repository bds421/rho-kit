package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
// Returns an error if the prefix contains invalid characters or is too long.
// The combined prefix+key must fit within MaxKeyLen (checked per-operation
// in fullKey). A prefix longer than MaxKeyLen/2 is rejected upfront to
// guarantee at least MaxKeyLen/2 bytes remain for keys.
func NewTypedCache[T any](backend Cache, prefix string) (*TypedCache[T], error) {
	if strings.ContainsAny(prefix, "\x00\n\r") {
		return nil, fmt.Errorf("cache prefix contains invalid characters (null byte, newline, or carriage return)")
	}
	if len(prefix) > MaxKeyLen/2 {
		return nil, fmt.Errorf("cache prefix length %d exceeds maximum of %d bytes", len(prefix), MaxKeyLen/2)
	}
	return &TypedCache[T]{backend: backend, prefix: prefix}, nil
}

// fullKey validates the user-provided key and returns the combined prefix+key.
// Validates both the key itself and the combined length.
func (tc *TypedCache[T]) fullKey(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	full := tc.prefix + key
	if len(full) > MaxKeyLen {
		return "", fmt.Errorf("cache key with prefix exceeds maximum length of %d bytes (prefix=%d, key=%d)",
			MaxKeyLen, len(tc.prefix), len(key))
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
