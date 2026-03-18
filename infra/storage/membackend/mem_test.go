package membackend

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
)

func TestMemBackend_PutAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	content := []byte("hello world")
	err := b.Put(ctx, "test.txt", bytes.NewReader(content), storage.ObjectMeta{ContentType: "text/plain"})
	require.NoError(t, err)

	rc, meta, err := b.Get(ctx, "test.txt")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, content, got)
	assert.Equal(t, "text/plain", meta.ContentType)
	assert.Equal(t, int64(11), meta.Size)
}

func TestMemBackend_GetNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	_, _, err := b.Get(ctx, "missing.txt")
	assert.ErrorIs(t, err, storage.ErrObjectNotFound)
}

func TestMemBackend_DeleteIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	assert.NoError(t, b.Delete(ctx, "nonexistent.txt"))
}

func TestMemBackend_Exists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	ok, err := b.Exists(ctx, "missing.txt")
	require.NoError(t, err)
	assert.False(t, ok)

	err = b.Put(ctx, "file.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
	require.NoError(t, err)

	ok, err = b.Exists(ctx, "file.txt")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestMemBackend_Copy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	err := b.Put(ctx, "src.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
	require.NoError(t, err)

	err = b.Copy(ctx, "src.txt", "dst.txt")
	require.NoError(t, err)

	rc, _, err := b.Get(ctx, "dst.txt")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, _ := io.ReadAll(rc)
	assert.Equal(t, []byte("data"), got)
}

func TestMemBackend_List(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	for _, key := range []string{"a.txt", "b.txt", "sub/c.txt"} {
		err := b.Put(ctx, key, bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.NoError(t, err)
	}

	var keys []string
	for info, err := range b.List(ctx, "sub/", storage.ListOptions{}) {
		require.NoError(t, err)
		keys = append(keys, info.Key)
	}
	assert.Equal(t, []string{"sub/c.txt"}, keys)
}

func TestMemBackend_ListMaxKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	for _, key := range []string{"a.txt", "b.txt", "c.txt"} {
		err := b.Put(ctx, key, bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.NoError(t, err)
	}

	var keys []string
	for info, err := range b.List(ctx, "", storage.ListOptions{MaxKeys: 2}) {
		require.NoError(t, err)
		keys = append(keys, info.Key)
	}
	assert.Len(t, keys, 2)
}

func TestMemBackend_LenAndReset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	err := b.Put(ctx, "a.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
	require.NoError(t, err)
	assert.Equal(t, 1, b.Len())

	b.Reset()
	assert.Equal(t, 0, b.Len())
}

func TestMemBackend_ImmutableStorage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	original := []byte("original")
	err := b.Put(ctx, "file.txt", bytes.NewReader(original), storage.ObjectMeta{})
	require.NoError(t, err)

	// Get data and mutate the returned bytes.
	rc, _, err := b.Get(ctx, "file.txt")
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	data[0] = 'X'

	// Verify stored data is unchanged.
	rc2, _, err := b.Get(ctx, "file.txt")
	require.NoError(t, err)
	defer func() { _ = rc2.Close() }()
	data2, _ := io.ReadAll(rc2)
	assert.Equal(t, original, data2)
}

func TestMemBackend_EmptyKeyRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	assert.Error(t, b.Put(ctx, "", bytes.NewReader(nil), storage.ObjectMeta{}))

	_, _, err := b.Get(ctx, "")
	assert.Error(t, err)

	assert.Error(t, b.Delete(ctx, ""))

	_, err = b.Exists(ctx, "")
	assert.Error(t, err)
}
