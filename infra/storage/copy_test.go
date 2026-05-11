package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/localbackend"
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

	t.Run("rejects nil backend", func(t *testing.T) {
		t.Parallel()
		err := storage.Copy(ctx, nil, "src.txt", "dst.txt")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "backend is required")
	})

	t.Run("returns error for missing source", func(t *testing.T) {
		t.Parallel()
		err := storage.Copy(ctx, backend, "nonexistent.txt", "dst2.txt")
		assert.Error(t, err)
	})

	t.Run("uses native copier discovered through unwrap chain", func(t *testing.T) {
		t.Parallel()
		inner := &nativeCopyProbe{}
		wrapped := unwrapOnlyStorage{Storage: inner}
		_, direct := any(wrapped).(storage.Copier)
		require.False(t, direct)

		err := storage.Copy(ctx, wrapped, "source.txt", "dest.txt")
		require.NoError(t, err)
		assert.True(t, inner.copied)
		assert.Equal(t, "source.txt", inner.srcKey)
		assert.Equal(t, "dest.txt", inner.dstKey)
	})
}

func TestCopy_GetSourceErrorDoesNotReflectKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := &fallbackCopyProbe{getErr: storage.ErrObjectNotFound}

	err := storage.Copy(ctx, backend, "secret-token.txt", "dst.txt")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
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

	t.Run("rejects nil source", func(t *testing.T) {
		t.Parallel()
		err := storage.CopyAcross(ctx, nil, "cross.txt", dst, "received.txt")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "source backend is required")
	})

	t.Run("rejects nil destination", func(t *testing.T) {
		t.Parallel()
		err := storage.CopyAcross(ctx, src, "cross.txt", nil, "received.txt")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "destination backend is required")
	})
}

func TestCopyAcross_PutDestinationErrorDoesNotReflectKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := &fallbackCopyProbe{}
	dst := &fallbackCopyProbe{putErr: errors.New("backend down")}

	err := storage.CopyAcross(ctx, src, "src.txt", dst, "secret-token.txt")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

func newTestBackend(t *testing.T) *localbackend.LocalBackend {
	t.Helper()
	b, err := localbackend.New(t.TempDir())
	require.NoError(t, err)
	return b
}

type nativeCopyProbe struct {
	copied bool
	srcKey string
	dstKey string
}

func (n *nativeCopyProbe) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return errors.New("fallback Put must not be called")
}

func (n *nativeCopyProbe) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	return nil, storage.ObjectMeta{}, errors.New("fallback Get must not be called")
}

func (n *nativeCopyProbe) Delete(context.Context, string) error { return nil }

func (n *nativeCopyProbe) Exists(context.Context, string) (bool, error) { return false, nil }

func (n *nativeCopyProbe) Copy(_ context.Context, srcKey, dstKey string) error {
	n.copied = true
	n.srcKey = srcKey
	n.dstKey = dstKey
	return nil
}

type fallbackCopyProbe struct {
	getErr error
	putErr error
}

func (p *fallbackCopyProbe) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return p.putErr
}

func (p *fallbackCopyProbe) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	if p.getErr != nil {
		return nil, storage.ObjectMeta{}, p.getErr
	}
	return io.NopCloser(bytes.NewReader([]byte("data"))), storage.ObjectMeta{Size: 4}, nil
}

func (p *fallbackCopyProbe) Delete(context.Context, string) error { return nil }

func (p *fallbackCopyProbe) Exists(context.Context, string) (bool, error) { return false, nil }
