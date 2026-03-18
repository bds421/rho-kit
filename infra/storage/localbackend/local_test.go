package localbackend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
)

func TestNew(t *testing.T) {
	t.Parallel()

	t.Run("creates root directory", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "new-root")
		b, err := New(dir)
		require.NoError(t, err)
		require.NotNil(t, b)

		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})

	t.Run("panics on empty dir", func(t *testing.T) {
		t.Parallel()
		assert.Panics(t, func() {
			_, _ = New("")
		})
	})
}

func TestLocalBackend_Put(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("stores and retrieves content", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)

		content := []byte("hello storage")
		err := b.Put(ctx, "test.txt", bytes.NewReader(content), storage.ObjectMeta{})
		require.NoError(t, err)

		// Verify the file exists on disk.
		data, err := os.ReadFile(filepath.Join(b.root, "test.txt"))
		require.NoError(t, err)
		assert.Equal(t, content, data)
	})

	t.Run("creates nested directories", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)

		err := b.Put(ctx, "a/b/c/deep.txt", bytes.NewReader([]byte("deep")), storage.ObjectMeta{})
		require.NoError(t, err)

		data, err := os.ReadFile(filepath.Join(b.root, "a", "b", "c", "deep.txt"))
		require.NoError(t, err)
		assert.Equal(t, []byte("deep"), data)
	})

	t.Run("overwrites existing key", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)

		require.NoError(t, b.Put(ctx, "key", bytes.NewReader([]byte("v1")), storage.ObjectMeta{}))
		require.NoError(t, b.Put(ctx, "key", bytes.NewReader([]byte("v2")), storage.ObjectMeta{}))

		data, err := os.ReadFile(filepath.Join(b.root, "key"))
		require.NoError(t, err)
		assert.Equal(t, []byte("v2"), data)
	})

	t.Run("rejects empty key", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)

		err := b.Put(ctx, "", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.Error(t, err)
	})

	t.Run("applies validators", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t, WithValidators(storage.MaxFileSize(5)))

		err := b.Put(ctx, "big.txt", bytes.NewReader([]byte("123456")), storage.ObjectMeta{})
		require.Error(t, err)
		assert.True(t, errors.Is(err, storage.ErrValidation))
	})
}

func TestLocalBackend_Get(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("returns content and metadata", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		content := []byte("hello")
		require.NoError(t, b.Put(ctx, "file.txt", bytes.NewReader(content), storage.ObjectMeta{}))

		rc, meta, err := b.Get(ctx, "file.txt")
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()

		got, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, content, got)
		assert.Equal(t, int64(5), meta.Size)
	})

	t.Run("returns ErrObjectNotFound for missing key", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)

		_, _, err := b.Get(ctx, "missing")
		require.Error(t, err)
		assert.True(t, errors.Is(err, storage.ErrObjectNotFound))
	})

	t.Run("rejects empty key", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)

		_, _, err := b.Get(ctx, "")
		require.Error(t, err)
	})
}

func TestLocalBackend_Delete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("deletes existing key", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		require.NoError(t, b.Put(ctx, "file.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{}))

		require.NoError(t, b.Delete(ctx, "file.txt"))

		exists, err := b.Exists(ctx, "file.txt")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("idempotent on missing key", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		assert.NoError(t, b.Delete(ctx, "never-existed"))
	})

	t.Run("rejects empty key", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		assert.Error(t, b.Delete(ctx, ""))
	})
}

func TestLocalBackend_Exists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("returns true for existing key", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		require.NoError(t, b.Put(ctx, "file.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{}))

		exists, err := b.Exists(ctx, "file.txt")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("returns false for missing key", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)

		exists, err := b.Exists(ctx, "missing")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func newBackend(t *testing.T, opts ...Option) *LocalBackend {
	t.Helper()
	b, err := New(t.TempDir(), opts...)
	require.NoError(t, err)
	return b
}
