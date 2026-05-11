package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"iter"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
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

	t.Run("invalid key is rejected before hooks", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		beforeCalled := false
		afterCalled := false
		hooked := storage.WithHooks(backend, storage.Hooks{
			BeforePut: func(context.Context, string, storage.ObjectMeta) error {
				beforeCalled = true
				return nil
			},
			AfterPut: func(context.Context, string, storage.ObjectMeta) {
				afterCalled = true
			},
		})

		err := hooked.Put(ctx, "bad key", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.ErrorIs(t, err, storage.ErrValidation)
		assert.False(t, beforeCalled)
		assert.False(t, afterCalled)
	})

	t.Run("hooks cannot mutate caller metadata", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		meta := storage.ObjectMeta{Custom: map[string]string{"tenant": "acme"}}
		hooked := storage.WithHooks(backend, storage.Hooks{
			BeforePut: func(_ context.Context, _ string, got storage.ObjectMeta) error {
				got.Custom["tenant"] = "evil"
				got.Custom["before"] = "mutated"
				return nil
			},
			AfterPut: func(_ context.Context, _ string, got storage.ObjectMeta) {
				got.Custom["after"] = "mutated"
			},
		})

		require.NoError(t, hooked.Put(ctx, "meta.txt", bytes.NewReader([]byte("x")), meta))
		assert.Equal(t, map[string]string{"tenant": "acme"}, meta.Custom)
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

	t.Run("invalid list options are rejected before backend list", func(t *testing.T) {
		t.Parallel()
		backend := &listValidationProbe{}
		hooked := storage.WithHooks(backend, storage.Hooks{})
		lister, ok := hooked.(storage.Lister)
		require.True(t, ok)

		var seenErr error
		for _, err := range lister.List(ctx, "", storage.ListOptions{MaxKeys: -1}) {
			seenErr = err
			break
		}

		require.ErrorIs(t, seenErr, storage.ErrValidation)
		assert.Equal(t, int32(0), backend.calls.Load())
	})

	t.Run("forwards Copier interface", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		hooked := storage.WithHooks(backend, storage.Hooks{})

		_, ok := hooked.(storage.Copier)
		assert.True(t, ok, "wrapped backend should implement Copier")
	})

	t.Run("forwards capabilities discovered through unwrap chain", func(t *testing.T) {
		t.Parallel()
		backend := newTestBackend(t)
		require.NoError(t, backend.Put(ctx, "source.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{}))
		wrapped := unwrapOnlyStorage{Storage: backend}
		_, directLister := any(wrapped).(storage.Lister)
		_, directCopier := any(wrapped).(storage.Copier)
		require.False(t, directLister)
		require.False(t, directCopier)

		var copied bool
		hooked := storage.WithHooks(wrapped, storage.Hooks{
			AfterCopy: func(context.Context, string, string) {
				copied = true
			},
		})

		lister, ok := hooked.(storage.Lister)
		require.True(t, ok, "WithHooks must preserve Lister found via Unwrap")
		var listed []string
		for info, err := range lister.List(ctx, "", storage.ListOptions{}) {
			require.NoError(t, err)
			listed = append(listed, info.Key)
		}
		assert.Contains(t, listed, "source.txt")

		copier, ok := hooked.(storage.Copier)
		require.True(t, ok, "WithHooks must preserve Copier found via Unwrap")
		require.NoError(t, copier.Copy(ctx, "source.txt", "copy.txt"))
		assert.True(t, copied)
		ok, err := backend.Exists(ctx, "copy.txt")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("nil backend panics at construction", func(t *testing.T) {
		t.Parallel()
		assert.PanicsWithValue(t, "storage: WithHooks requires a non-nil backend", func() {
			_ = storage.WithHooks(nil, storage.Hooks{})
		})
	})
}

type unwrapOnlyStorage struct {
	storage.Storage
}

func (u unwrapOnlyStorage) Unwrap() storage.Storage {
	return u.Storage
}

type listValidationProbe struct {
	calls atomic.Int32
}

func (p *listValidationProbe) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	p.calls.Add(1)
	return nil
}

func (p *listValidationProbe) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	p.calls.Add(1)
	return nil, storage.ObjectMeta{}, errors.New("backend should not be called")
}

func (p *listValidationProbe) Delete(context.Context, string) error {
	p.calls.Add(1)
	return nil
}

func (p *listValidationProbe) Exists(context.Context, string) (bool, error) {
	p.calls.Add(1)
	return false, nil
}

func (p *listValidationProbe) List(context.Context, string, storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		p.calls.Add(1)
		yield(storage.ObjectInfo{}, errors.New("backend should not be called"))
	}
}
