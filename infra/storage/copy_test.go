package storage_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
	"github.com/bds421/rho-kit/infra/storage/localbackend"
)

func TestCopy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := newTestBackend(t)

	err := backend.Put(ctx, "src.txt", bytes.NewReader([]byte("hello")), storage.ObjectMeta{ContentType: "text/plain"})
	require.NoError(t, err)

	t.Run("copies object", func(t *testing.T) {
		t.Parallel()
		err := storage.Copy(ctx, backend, "src.txt", "dst.txt")
		require.NoError(t, err)

		rc, meta, err := backend.Get(ctx, "dst.txt")
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()
		got, _ := io.ReadAll(rc)
		assert.Equal(t, []byte("hello"), got)
		assert.Equal(t, int64(5), meta.Size)
	})

	t.Run("rejects empty source key", func(t *testing.T) {
		t.Parallel()
		err := storage.Copy(ctx, backend, "", "dst.txt")
		assert.Error(t, err)
	})

	t.Run("rejects empty destination key", func(t *testing.T) {
		t.Parallel()
		err := storage.Copy(ctx, backend, "src.txt", "")
		assert.Error(t, err)
	})

	t.Run("returns error for missing source", func(t *testing.T) {
		t.Parallel()
		err := storage.Copy(ctx, backend, "nonexistent.txt", "dst2.txt")
		assert.Error(t, err)
	})
}

func TestMove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := newTestBackend(t)

	err := backend.Put(ctx, "move-src.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
	require.NoError(t, err)

	err = storage.Move(ctx, backend, "move-src.txt", "move-dst.txt")
	require.NoError(t, err)

	// Destination exists.
	rc, _, err := backend.Get(ctx, "move-dst.txt")
	require.NoError(t, err)
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, []byte("data"), got)

	// Source is gone.
	ok, err := backend.Exists(ctx, "move-src.txt")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCopyAcross(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := newTestBackend(t)
	dst := newTestBackend(t)

	err := src.Put(ctx, "cross.txt", bytes.NewReader([]byte("cross")), storage.ObjectMeta{ContentType: "text/plain"})
	require.NoError(t, err)

	err = storage.CopyAcross(ctx, src, "cross.txt", dst, "received.txt")
	require.NoError(t, err)

	rc, meta, err := dst.Get(ctx, "received.txt")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	assert.Equal(t, []byte("cross"), got)
	assert.Equal(t, int64(5), meta.Size)
}

func newTestBackend(t *testing.T) *localbackend.LocalBackend {
	t.Helper()
	b, err := localbackend.New(t.TempDir())
	require.NoError(t, err)
	return b
}
