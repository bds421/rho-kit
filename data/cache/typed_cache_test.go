package cache

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testUser struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func newTestTypedCache[T any](t *testing.T, backend Cache, prefix string) *TypedCache[T] {
	t.Helper()
	tc, err := NewTypedCache[T](backend, prefix)
	require.NoError(t, err)
	return tc
}

func TestTypedCache_SetAndGet(t *testing.T) {
	cache := MustNewMemoryCache()
	typed := newTestTypedCache[testUser](t, cache, "user:")
	ctx := context.Background()

	user := testUser{Name: "Alice", Email: "alice@example.com"}
	err := typed.Set(ctx, "1", user, time.Minute)
	require.NoError(t, err)
	cache.Sync()

	got, err := typed.Get(ctx, "1")
	require.NoError(t, err)
	assert.Equal(t, user, got)
}

func TestTypedCache_GetMiss(t *testing.T) {
	cache := MustNewMemoryCache()
	typed := newTestTypedCache[testUser](t, cache, "user:")
	ctx := context.Background()

	_, err := typed.Get(ctx, "nonexistent")
	assert.True(t, errors.Is(err, ErrCacheMiss))
}

func TestTypedCache_Delete(t *testing.T) {
	cache := MustNewMemoryCache()
	typed := newTestTypedCache[testUser](t, cache, "user:")
	ctx := context.Background()

	user := testUser{Name: "Bob", Email: "bob@example.com"}
	_ = typed.Set(ctx, "2", user, time.Minute)
	cache.Sync()

	err := typed.Delete(ctx, "2")
	require.NoError(t, err)

	_, err = typed.Get(ctx, "2")
	assert.True(t, errors.Is(err, ErrCacheMiss))
}

func TestTypedCache_Prefix(t *testing.T) {
	cache := MustNewMemoryCache()
	typed := newTestTypedCache[string](t, cache, "prefix:")
	ctx := context.Background()

	_ = typed.Set(ctx, "key", "value", time.Minute)
	cache.Sync()

	// Should be stored with prefix in the underlying cache.
	raw, err := cache.Get(ctx, "prefix:key")
	require.NoError(t, err)
	assert.Contains(t, string(raw), "value")

	// Should not be found without prefix.
	_, err = cache.Get(ctx, "key")
	assert.True(t, errors.Is(err, ErrCacheMiss))
}

func TestTypedCache_Exists(t *testing.T) {
	cache := MustNewMemoryCache()
	typed := newTestTypedCache[testUser](t, cache, "user:")
	ctx := context.Background()

	user := testUser{Name: "Charlie", Email: "charlie@example.com"}
	_ = typed.Set(ctx, "3", user, time.Minute)
	cache.Sync()

	exists, err := typed.Exists(ctx, "3")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = typed.Exists(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestTypedCache_InvalidJSON(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	// Write invalid JSON directly to the backend.
	_ = cache.Set(ctx, "user:bad", []byte("not json"), time.Minute)
	cache.Sync()

	typed := newTestTypedCache[testUser](t, cache, "user:")
	_, err := typed.Get(ctx, "bad")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cache unmarshal")
}

func TestTypedCache_EmptyPrefix(t *testing.T) {
	cache := MustNewMemoryCache()
	typed := newTestTypedCache[string](t, cache, "")
	ctx := context.Background()

	_ = typed.Set(ctx, "key", "value", time.Minute)
	cache.Sync()

	raw, err := cache.Get(ctx, "key")
	require.NoError(t, err)
	assert.Contains(t, string(raw), "value")
}

func TestNewTypedCache_InvalidPrefix_NullByte(t *testing.T) {
	_, err := NewTypedCache[string](MustNewMemoryCache(), "bad\x00prefix")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestNewTypedCache_InvalidPrefix_TooLong(t *testing.T) {
	longPrefix := strings.Repeat("x", MaxKeyLen/2+1)
	_, err := NewTypedCache[string](MustNewMemoryCache(), longPrefix)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

func TestNewTypedCache_ValidLongPrefix(t *testing.T) {
	prefix := strings.Repeat("x", MaxKeyLen/2)
	_, err := NewTypedCache[string](MustNewMemoryCache(), prefix)
	assert.NoError(t, err)
}

func TestTypedCache_CombinedKeyTooLong(t *testing.T) {
	// Prefix of 400 bytes + key of 625 bytes = 1025 > MaxKeyLen (1024).
	prefix := strings.Repeat("p", 400)
	tc := newTestTypedCache[string](t, MustNewMemoryCache(), prefix)
	ctx := context.Background()

	longKey := strings.Repeat("k", 625)
	err := tc.Set(ctx, longKey, "value", time.Minute)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "with prefix exceeds maximum length")

	_, err = tc.Get(ctx, longKey)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "with prefix exceeds maximum length")

	err = tc.Delete(ctx, longKey)
	assert.Error(t, err)

	_, err = tc.Exists(ctx, longKey)
	assert.Error(t, err)
}

func TestTypedCache_EmptyKeyRejected(t *testing.T) {
	tc := newTestTypedCache[string](t, MustNewMemoryCache(), "pfx:")
	ctx := context.Background()

	err := tc.Set(ctx, "", "v", time.Minute)
	assert.ErrorIs(t, err, ErrKeyEmpty)
}
