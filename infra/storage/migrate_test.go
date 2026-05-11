package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/membackend"
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

func TestMigrate_NilBackendsReturnError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.New()

	_, err := storage.Migrate(ctx, nil, backend, storage.MigrateOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "source backend is required")

	_, err = storage.Migrate(ctx, backend, nil, storage.MigrateOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "destination backend is required")
}

func TestMigrate_RejectsInvalidListedSourceKeyBeforeGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := &unsafeListerBackend{key: "secret-token bad"}
	dst := membackend.New()

	result, err := storage.Migrate(ctx, src, dst, storage.MigrateOptions{
		KeyTransform: func(string) string {
			return "safe.txt"
		},
	})

	require.NoError(t, err)
	assert.Equal(t, int64(0), result.Copied)
	assert.Equal(t, int64(1), result.Failed)
	assert.Equal(t, 0, src.getCalls)
	require.ErrorIs(t, result.Errors["secret-token bad"], storage.ErrValidation)
	assert.NotContains(t, result.Errors["secret-token bad"].Error(), "secret-token")
}

func TestMigrate_RetainedErrorsAreCapped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := &manyUnsafeListerBackend{count: storage.MaxMigrationErrors + 2}
	dst := membackend.New()
	var progressCalls int

	result, err := storage.Migrate(ctx, src, dst, storage.MigrateOptions{
		OnProgress: func(string, bool, error) {
			progressCalls++
		},
	})

	require.NoError(t, err)
	assert.Equal(t, int64(storage.MaxMigrationErrors+2), result.Failed)
	assert.Len(t, result.Errors, storage.MaxMigrationErrors)
	assert.True(t, result.ErrorsTruncated)
	assert.Equal(t, storage.MaxMigrationErrors+2, progressCalls)
	assert.Equal(t, 0, src.getCalls)
}

func TestMigrate_RejectsInvalidTransformedKeyWithoutReflectingIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := &unsafeListerBackend{key: "safe.txt"}
	dst := membackend.New()

	result, err := storage.Migrate(ctx, src, dst, storage.MigrateOptions{
		KeyTransform: func(string) string {
			return "secret-token bad"
		},
	})

	require.NoError(t, err)
	assert.Equal(t, int64(0), result.Copied)
	assert.Equal(t, int64(1), result.Failed)
	assert.Equal(t, 0, src.getCalls)
	require.ErrorIs(t, result.Errors["safe.txt"], storage.ErrValidation)
	assert.NotContains(t, result.Errors["safe.txt"].Error(), "secret-token")
}

func TestMigrate_CopyGetErrorDoesNotReflectSourceKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := &unsafeListerBackend{key: "secret-token.txt", getErr: errors.New("backend down")}
	dst := membackend.New()

	result, err := storage.Migrate(ctx, src, dst, storage.MigrateOptions{})

	require.NoError(t, err)
	assert.Equal(t, int64(0), result.Copied)
	assert.Equal(t, int64(1), result.Failed)
	require.Error(t, result.Errors["secret-token.txt"])
	assert.NotContains(t, result.Errors["secret-token.txt"].Error(), "secret-token")
}

func TestMigrate_CopyPutErrorDoesNotReflectDestinationKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := &unsafeListerBackend{key: "source.txt"}
	dst := &putFailBackend{}

	result, err := storage.Migrate(ctx, src, dst, storage.MigrateOptions{
		KeyTransform: func(string) string {
			return "secret-token.txt"
		},
	})

	require.NoError(t, err)
	assert.Equal(t, int64(0), result.Copied)
	assert.Equal(t, int64(1), result.Failed)
	require.Error(t, result.Errors["source.txt"])
	assert.NotContains(t, result.Errors["source.txt"].Error(), "secret-token")
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

func TestMigrateCount_RejectsInvalidListedSourceKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := &unsafeListerBackend{key: "secret-token bad"}

	count, err := storage.MigrateCount(ctx, src, "")

	assert.Equal(t, int64(0), count)
	require.ErrorIs(t, err, storage.ErrValidation)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestMigrateCount_NilBackendReturnsError(t *testing.T) {
	t.Parallel()

	count, err := storage.MigrateCount(context.Background(), nil, "")

	assert.Equal(t, int64(0), count)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "source backend is required")
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

type unsafeListerBackend struct {
	key      string
	getErr   error
	getCalls int
}

func (b *unsafeListerBackend) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return nil
}

func (b *unsafeListerBackend) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	b.getCalls++
	if b.getErr != nil {
		return nil, storage.ObjectMeta{}, b.getErr
	}
	return io.NopCloser(strings.NewReader("data")), storage.ObjectMeta{}, nil
}

func (b *unsafeListerBackend) Delete(context.Context, string) error { return nil }

func (b *unsafeListerBackend) Exists(context.Context, string) (bool, error) {
	return false, nil
}

func (b *unsafeListerBackend) List(context.Context, string, storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		yield(storage.ObjectInfo{Key: b.key}, nil)
	}
}

type manyUnsafeListerBackend struct {
	count    int
	getCalls int
}

func (b *manyUnsafeListerBackend) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return nil
}

func (b *manyUnsafeListerBackend) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	b.getCalls++
	return io.NopCloser(strings.NewReader("data")), storage.ObjectMeta{}, nil
}

func (b *manyUnsafeListerBackend) Delete(context.Context, string) error { return nil }

func (b *manyUnsafeListerBackend) Exists(context.Context, string) (bool, error) {
	return false, nil
}

func (b *manyUnsafeListerBackend) List(context.Context, string, storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		for i := 0; i < b.count; i++ {
			if !yield(storage.ObjectInfo{Key: fmt.Sprintf("bad key-%d", i)}, nil) {
				return
			}
		}
	}
}

type putFailBackend struct{}

func (b *putFailBackend) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return errors.New("backend down")
}

func (b *putFailBackend) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	return nil, storage.ObjectMeta{}, storage.ErrObjectNotFound
}

func (b *putFailBackend) Delete(context.Context, string) error { return nil }

func (b *putFailBackend) Exists(context.Context, string) (bool, error) { return false, nil }
