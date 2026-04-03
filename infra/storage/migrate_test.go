package storage_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
	"github.com/bds421/rho-kit/infra/storage/membackend"
)

func TestMigrate_CopiesAllObjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	src := membackend.New()
	dst := membackend.New()

	require.NoError(t, src.Put(ctx, "a.txt", bytes.NewReader([]byte("aaa")), storage.ObjectMeta{}))
	require.NoError(t, src.Put(ctx, "b.txt", bytes.NewReader([]byte("bbb")), storage.ObjectMeta{}))

	result, err := storage.Migrate(ctx, src, dst, storage.MigrateOptions{})
	require.NoError(t, err)

	assert.Equal(t, int64(2), result.Copied)
	assert.Equal(t, int64(0), result.Skipped)
	assert.Equal(t, int64(0), result.Failed)

	rc, _, err := dst.Get(ctx, "a.txt")
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, []byte("aaa"), data)
}

func TestMigrate_SkipsExisting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	src := membackend.New()
	dst := membackend.New()

	require.NoError(t, src.Put(ctx, "a.txt", bytes.NewReader([]byte("new")), storage.ObjectMeta{}))
	require.NoError(t, dst.Put(ctx, "a.txt", bytes.NewReader([]byte("old")), storage.ObjectMeta{}))

	result, err := storage.Migrate(ctx, src, dst, storage.MigrateOptions{Overwrite: false})
	require.NoError(t, err)

	assert.Equal(t, int64(0), result.Copied)
	assert.Equal(t, int64(1), result.Skipped)

	// Original content preserved.
	rc, _, err := dst.Get(ctx, "a.txt")
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, []byte("old"), data)
}

func TestMigrate_OverwriteExisting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	src := membackend.New()
	dst := membackend.New()

	require.NoError(t, src.Put(ctx, "a.txt", bytes.NewReader([]byte("new")), storage.ObjectMeta{}))
	require.NoError(t, dst.Put(ctx, "a.txt", bytes.NewReader([]byte("old")), storage.ObjectMeta{}))

	result, err := storage.Migrate(ctx, src, dst, storage.MigrateOptions{Overwrite: true})
	require.NoError(t, err)

	assert.Equal(t, int64(1), result.Copied)

	rc, _, err := dst.Get(ctx, "a.txt")
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, []byte("new"), data)
}

func TestMigrate_DryRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	src := membackend.New()
	dst := membackend.New()

	require.NoError(t, src.Put(ctx, "a.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{}))

	result, err := storage.Migrate(ctx, src, dst, storage.MigrateOptions{DryRun: true})
	require.NoError(t, err)

	assert.Equal(t, int64(0), result.Copied)
	assert.Equal(t, int64(1), result.Skipped)

	// Nothing actually copied.
	ok, err := dst.Exists(ctx, "a.txt")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestMigrate_KeyTransform(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	src := membackend.New()
	dst := membackend.New()

	require.NoError(t, src.Put(ctx, "old/a.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{}))

	result, err := storage.Migrate(ctx, src, dst, storage.MigrateOptions{
		Prefix: "old/",
		KeyTransform: func(key string) string {
			return strings.Replace(key, "old/", "new/", 1)
		},
	})
	require.NoError(t, err)

	assert.Equal(t, int64(1), result.Copied)

	ok, err := dst.Exists(ctx, "new/a.txt")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestMigrate_NonListerReturnsError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// circuitbreaker.CircuitBreaker doesn't implement Lister, but we need
	// a simpler example — just use a plain Storage mock.
	src := &nonListerBackend{}
	dst := membackend.New()

	_, err := storage.Migrate(ctx, src, dst, storage.MigrateOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not implement Lister")
}

func TestMigrateCount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	src := membackend.New()
	require.NoError(t, src.Put(ctx, "a.txt", bytes.NewReader([]byte("a")), storage.ObjectMeta{}))
	require.NoError(t, src.Put(ctx, "b.txt", bytes.NewReader([]byte("b")), storage.ObjectMeta{}))
	require.NoError(t, src.Put(ctx, "c.txt", bytes.NewReader([]byte("c")), storage.ObjectMeta{}))

	count, err := storage.MigrateCount(ctx, src, "")
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

// nonListerBackend is a minimal Storage that does NOT implement Lister.
type nonListerBackend struct{}

func (b *nonListerBackend) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return nil
}
func (b *nonListerBackend) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	return nil, storage.ObjectMeta{}, storage.ErrObjectNotFound
}
func (b *nonListerBackend) Delete(context.Context, string) error         { return nil }
func (b *nonListerBackend) Exists(context.Context, string) (bool, error) { return false, nil }
