package storage_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
)

func TestManager(t *testing.T) {
	t.Parallel()

	t.Run("register and retrieve disk", func(t *testing.T) {
		t.Parallel()
		b1 := newTestBackend(t)
		b2 := newTestBackend(t)

		mgr := storage.NewManager()
		mgr.Register("local", b1)
		mgr.Register("uploads", b2)

		assert.Equal(t, b1, mgr.Disk("local"))
		assert.Equal(t, b2, mgr.Disk("uploads"))
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

	t.Run("panics on empty name", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		assert.Panics(t, func() { mgr.Register("", newTestBackend(t)) })
	})

	t.Run("panics on nil backend", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		assert.Panics(t, func() { mgr.Register("test", nil) })
	})

	t.Run("panics on duplicate name", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		mgr.Register("dup", newTestBackend(t))
		assert.Panics(t, func() { mgr.Register("dup", newTestBackend(t)) })
	})

	t.Run("panics on unregistered disk", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		assert.Panics(t, func() { mgr.Disk("nonexistent") })
	})

	t.Run("panics on SetDefault with unregistered name", func(t *testing.T) {
		t.Parallel()
		mgr := storage.NewManager()
		assert.Panics(t, func() { mgr.SetDefault("nonexistent") })
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

		result := mgr.Register("disk", b).SetDefault("disk")
		require.NotNil(t, result)
		assert.Equal(t, b, mgr.Default())
	})
}

