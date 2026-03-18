package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
)

func TestWithHooks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("AfterPut is called after successful put", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		var calledKey string
		hooked := storage.WithHooks(backend, storage.Hooks{
			AfterPut: func(_ context.Context, key string, _ storage.ObjectMeta) {
				calledKey = key
			},
		})

		err := hooked.Put(ctx, "test.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
		require.NoError(t, err)
		assert.Equal(t, "test.txt", calledKey)
	})

	t.Run("BeforePut can abort", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		hooked := storage.WithHooks(backend, storage.Hooks{
			BeforePut: func(_ context.Context, key string, _ storage.ObjectMeta) error {
				if key == "blocked.txt" {
					return errors.New("blocked by hook")
				}
				return nil
			},
		})

		err := hooked.Put(ctx, "blocked.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "blocked by hook")

		// File was not stored.
		ok, _ := backend.Exists(ctx, "blocked.txt")
		assert.False(t, ok)
	})

	t.Run("AfterGet is called after successful get", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		require.NoError(t, backend.Put(ctx, "get-test.txt", bytes.NewReader([]byte("hello")), storage.ObjectMeta{}))

		var calledKey string
		hooked := storage.WithHooks(backend, storage.Hooks{
			AfterGet: func(_ context.Context, key string, _ storage.ObjectMeta) {
				calledKey = key
			},
		})

		rc, _, err := hooked.Get(ctx, "get-test.txt")
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()
		got, _ := io.ReadAll(rc)
		assert.Equal(t, []byte("hello"), got)
		assert.Equal(t, "get-test.txt", calledKey)
	})

	t.Run("AfterDelete is called after successful delete", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		require.NoError(t, backend.Put(ctx, "del.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{}))

		var calledKey string
		hooked := storage.WithHooks(backend, storage.Hooks{
			AfterDelete: func(_ context.Context, key string) {
				calledKey = key
			},
		})

		require.NoError(t, hooked.Delete(ctx, "del.txt"))
		assert.Equal(t, "del.txt", calledKey)
	})

	t.Run("BeforeDelete can abort", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		require.NoError(t, backend.Put(ctx, "keep.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{}))

		hooked := storage.WithHooks(backend, storage.Hooks{
			BeforeDelete: func(_ context.Context, key string) error {
				return errors.New("delete blocked")
			},
		})

		err := hooked.Delete(ctx, "keep.txt")
		require.Error(t, err)

		// File still exists.
		ok, _ := backend.Exists(ctx, "keep.txt")
		assert.True(t, ok)
	})

	t.Run("Exists passes through without hooks", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		require.NoError(t, backend.Put(ctx, "exist.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{}))

		hooked := storage.WithHooks(backend, storage.Hooks{})

		ok, err := hooked.Exists(ctx, "exist.txt")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("hooks are not called on backend error", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		afterCalled := false
		hooked := storage.WithHooks(backend, storage.Hooks{
			AfterGet: func(_ context.Context, _ string, _ storage.ObjectMeta) {
				afterCalled = true
			},
		})

		_, _, err := hooked.Get(ctx, "nonexistent.txt")
		require.Error(t, err)
		assert.False(t, afterCalled)
	})

	t.Run("forwards Lister interface", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		hooked := storage.WithHooks(backend, storage.Hooks{})

		_, ok := hooked.(storage.Lister)
		assert.True(t, ok, "wrapped backend should implement Lister")
	})

	t.Run("forwards Copier interface", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		hooked := storage.WithHooks(backend, storage.Hooks{})

		_, ok := hooked.(storage.Copier)
		assert.True(t, ok, "wrapped backend should implement Copier")
	})
}
