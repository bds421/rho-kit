package storage_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

func TestManager(t *testing.T) {
	t.Parallel()

	t.Run("register and retrieve backend", func(t *testing.T) {
		t.Parallel()
		b1 := newTestBackend(t)
		b2 := newTestBackend(t)

		mgr := storage.NewManager()
		mgr.Register("local", b1)
		mgr.Register("uploads", b2)

		got1, err := mgr.Backend("local")
		require.NoError(t, err)
		assert.Equal(t, b1, got1)

		got2, err := mgr.Backend("uploads")
		require.NoError(t, err)
		assert.Equal(t, b2, got2)
	})

	t.Run("Backend returns NotFound for unregistered name", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		mgr.Register("known", newTestBackend(t))

		got, err := mgr.Backend("unknown")
		require.Error(t, err)
		assert.Nil(t, got)
		var nfe *apperror.NotFoundError
		assert.True(t, errors.As(err, &nfe), "Backend miss should be apperror.NotFoundError")
	})

	t.Run("MustBackend panics on unregistered name", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		assert.Panics(t, func() { mgr.MustBackend("nonexistent-secret-token") })
	})

	t.Run("MustBackend returns backend on hit", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		b := newTestBackend(t)
		mgr.Register("hit", b)
		assert.Equal(t, b, mgr.MustBackend("hit"))
	})

	t.Run("default returns first registered", func(t *testing.T) {
		t.Parallel()
		b1 := newTestBackend(t)
		b2 := newTestBackend(t)

		mgr := storage.NewManager()
		mgr.Register("first", b1)
		mgr.Register("second", b2)

		assert.Equal(t, b1, mgr.Default())
	})

	t.Run("SetDefault overrides first-registered default", func(t *testing.T) {
		t.Parallel()
		b1 := newTestBackend(t)
		b2 := newTestBackend(t)

		mgr := storage.NewManager()
		mgr.Register("first", b1)
		mgr.Register("second", b2)
		mgr.SetDefault("second")

		assert.Equal(t, b2, mgr.Default())
	})

	t.Run("Names returns sorted names", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		mgr.Register("c", newTestBackend(t))
		mgr.Register("a", newTestBackend(t))
		mgr.Register("b", newTestBackend(t))

		assert.Equal(t, []string{"a", "b", "c"}, mgr.Names())
	})

	t.Run("Has reports existence", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		mgr.Register("exists", newTestBackend(t))

		assert.True(t, mgr.Has("exists"))
		assert.False(t, mgr.Has("nope"))
	})

	t.Run("nil Close is no-op", func(t *testing.T) {
		t.Parallel()
		var mgr *storage.Manager
		assert.NoError(t, mgr.Close())
	})

	t.Run("Close error does not reflect backend name", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		mgr.Register("secret-token-backend", closeFailBackend{})

		err := mgr.Close()

		require.Error(t, err)
		assert.NotContains(t, err.Error(), "secret-token")
	})

	t.Run("panics on empty name", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		assert.Panics(t, func() { mgr.Register("", newTestBackend(t)) })
	})

	t.Run("panics on nil backend", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		assert.PanicsWithValue(t, "storage.Manager: backend must not be nil", func() {
			mgr.Register("test-secret-token", nil)
		})
	})

	t.Run("panics on duplicate name", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		mgr.Register("dup-secret-token", newTestBackend(t))
		assert.PanicsWithValue(t, "storage.Manager: backend already registered", func() {
			mgr.Register("dup-secret-token", newTestBackend(t))
		})
	})

	t.Run("panics on SetDefault with unregistered name", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		assert.PanicsWithValue(t, "storage.Manager: default backend is not registered", func() {
			mgr.SetDefault("nonexistent-secret-token")
		})
	})

	t.Run("panics on Default with no backends", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		assert.Panics(t, func() { mgr.Default() })
	})

	t.Run("fluent chaining", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		b := newTestBackend(t)

		result := mgr.Register("backend", b).SetDefault("backend")
		require.NotNil(t, result)
		assert.Equal(t, b, mgr.Default())
	})
}

type closeFailBackend struct{}

func (closeFailBackend) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return nil
}

func (closeFailBackend) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	return nil, storage.ObjectMeta{}, storage.ErrObjectNotFound
}

func (closeFailBackend) Delete(context.Context, string) error { return nil }

func (closeFailBackend) Exists(context.Context, string) (bool, error) { return false, nil }

func (closeFailBackend) Close() error { return errors.New("close failed") }
