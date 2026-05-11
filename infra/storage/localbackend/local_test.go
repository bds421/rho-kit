package localbackend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
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

	t.Run("panics on nil option", func(t *testing.T) {
		t.Parallel()
		assert.Panics(t, func() {
			_, _ = New(t.TempDir(), nil)
		})
	})

	t.Run("panics on nil validator", func(t *testing.T) {
		t.Parallel()
		assert.Panics(t, func() {
			_, _ = New(t.TempDir(), WithValidators(nil))
		})
	})

	t.Run("validator option detaches caller slice", func(t *testing.T) {
		t.Parallel()
		var calls []string
		validators := []storage.Validator{
			func(_ context.Context, r io.Reader, meta *storage.ObjectMeta) (io.Reader, error) {
				calls = append(calls, "original")
				return r, nil
			},
		}
		opt := WithValidators(validators...)
		validators[0] = func(_ context.Context, r io.Reader, meta *storage.ObjectMeta) (io.Reader, error) {
			calls = append(calls, "mutated")
			return r, nil
		}

		b := newBackend(t, opt)
		require.NoError(t, b.Put(context.Background(), "detached.txt", strings.NewReader("ok"), storage.ObjectMeta{}))
		assert.Equal(t, []string{"original"}, calls)
	})

	t.Run("filesystem error does not reflect root path", func(t *testing.T) {
		t.Parallel()
		root := filepath.Join(t.TempDir(), "secret-token-root") + "\x00"

		_, err := New(root)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "secret-token-root")
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

	t.Run("closes validator-owned reader after success", func(t *testing.T) {
		t.Parallel()
		closed := false
		b := newBackend(t, WithValidators(func(context.Context, io.Reader, *storage.ObjectMeta) (io.Reader, error) {
			return &trackingReadCloser{Reader: strings.NewReader("validated"), closed: &closed}, nil
		}))

		err := b.Put(ctx, "validated.txt", strings.NewReader("original"), storage.ObjectMeta{})
		require.NoError(t, err)
		assert.True(t, closed)

		data, err := os.ReadFile(filepath.Join(b.root, "validated.txt"))
		require.NoError(t, err)
		assert.Equal(t, []byte("validated"), data)
	})

	t.Run("does not close caller-owned reader without validators", func(t *testing.T) {
		t.Parallel()
		closed := false
		b := newBackend(t)
		r := &trackingReadCloser{Reader: strings.NewReader("caller-owned"), closed: &closed}

		err := b.Put(ctx, "caller-owned.txt", r, storage.ObjectMeta{})
		require.NoError(t, err)
		assert.False(t, closed)
	})

	t.Run("rejects nil reader", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)

		err := b.Put(ctx, "nil.txt", nil, storage.ObjectMeta{})
		require.Error(t, err)
		assert.True(t, errors.Is(err, storage.ErrValidation))
	})

	t.Run("rejects symlinked parent", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := t.TempDir()
		link := filepath.Join(b.root, "secret-token-link")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}

		err := b.Put(ctx, "secret-token-link/owned.txt", bytes.NewReader([]byte("owned")), storage.ObjectMeta{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")
		assert.NotContains(t, err.Error(), "secret-token")

		_, statErr := os.Stat(filepath.Join(outside, "owned.txt"))
		assert.True(t, errors.Is(statErr, os.ErrNotExist))
	})

	t.Run("rejects symlinked parent before creating nested dirs", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := t.TempDir()
		link := filepath.Join(b.root, "link")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}

		err := b.Put(ctx, "link/sub/owned.txt", bytes.NewReader([]byte("owned")), storage.ObjectMeta{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")

		_, statErr := os.Stat(filepath.Join(outside, "sub"))
		assert.True(t, errors.Is(statErr, os.ErrNotExist))
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

		_, _, err := b.Get(ctx, "secret-token.txt")
		require.Error(t, err)
		assert.True(t, errors.Is(err, storage.ErrObjectNotFound))
		assert.NotContains(t, err.Error(), "secret-token")
	})

	t.Run("rejects empty key", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)

		_, _, err := b.Get(ctx, "")
		require.Error(t, err)
	})

	t.Run("rejects symlinked object", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := filepath.Join(t.TempDir(), "secret.txt")
		require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
		if err := os.Symlink(outside, filepath.Join(b.root, "secret-token-link")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}

		_, _, err := b.Get(ctx, "secret-token-link")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")
		assert.NotContains(t, err.Error(), "secret-token")
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

func TestLocalBackend_CopyRejectsSymlinkSource(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	if err := os.Symlink(outside, filepath.Join(b.root, "secret-token-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := b.Copy(context.Background(), "secret-token-link", "copy.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestLocalBackend_CopyRejectsSymlinkDestinationBeforeCreatingNestedDirs(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	ctx := context.Background()
	require.NoError(t, b.Put(ctx, "source.txt", bytes.NewReader([]byte("source")), storage.ObjectMeta{}))
	outside := t.TempDir()
	link := filepath.Join(b.root, "secret-token-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := b.Copy(ctx, "source.txt", "secret-token-link/sub/copy.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
	assert.NotContains(t, err.Error(), "secret-token")

	_, statErr := os.Stat(filepath.Join(outside, "sub"))
	assert.True(t, errors.Is(statErr, os.ErrNotExist))
}

func TestLocalBackend_FilesystemErrorsDoNotReflectRootPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := &LocalBackend{root: "secret-token-root\x00"}

	err := b.Put(ctx, "file.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token-root")

	_, _, err = b.Get(ctx, "file.txt")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token-root")

	exists, err := b.Exists(ctx, "file.txt")
	require.Error(t, err)
	assert.False(t, exists)
	assert.NotContains(t, err.Error(), "secret-token-root")

	err = b.Delete(ctx, "file.txt")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token-root")

	err = b.Copy(ctx, "file.txt", "copy.txt")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token-root")
}

func TestLocalBackend_ListRejectsSymlinkObject(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	if err := os.Symlink(outside, filepath.Join(b.root, "secret-token-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	var errs []error
	for _, err := range b.List(context.Background(), "", storage.ListOptions{}) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	require.NotEmpty(t, errs)
	assert.True(t, strings.Contains(errs[0].Error(), "symlink"))
	assert.NotContains(t, errs[0].Error(), "secret-token")
}

func TestLocalBackend_ListRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	var seenErr error
	for _, err := range b.List(context.Background(), "", storage.ListOptions{MaxKeys: -1}) {
		seenErr = err
		break
	}

	require.ErrorIs(t, seenErr, storage.ErrValidation)
}

func TestLocalBackend_RejectsSymlinkedRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("put", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := replaceRootWithSymlink(t, b)

		err := b.Put(ctx, "owned.txt", bytes.NewReader([]byte("owned")), storage.ObjectMeta{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")

		_, statErr := os.Stat(filepath.Join(outside, "owned.txt"))
		assert.True(t, errors.Is(statErr, os.ErrNotExist))
	})

	t.Run("get", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := replaceRootWithSymlink(t, b)
		require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))

		_, _, err := b.Get(ctx, "secret.txt")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")
	})

	t.Run("exists", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := replaceRootWithSymlink(t, b)
		require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))

		exists, err := b.Exists(ctx, "secret.txt")
		require.Error(t, err)
		assert.False(t, exists)
		assert.Contains(t, err.Error(), "symlink")
	})

	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := replaceRootWithSymlink(t, b)
		secretPath := filepath.Join(outside, "secret.txt")
		require.NoError(t, os.WriteFile(secretPath, []byte("secret"), 0o600))

		err := b.Delete(ctx, "secret.txt")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")

		_, statErr := os.Stat(secretPath)
		require.NoError(t, statErr)
	})

	t.Run("copy", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := replaceRootWithSymlink(t, b)
		require.NoError(t, os.WriteFile(filepath.Join(outside, "source.txt"), []byte("source"), 0o600))

		err := b.Copy(ctx, "source.txt", "copy.txt")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")

		_, statErr := os.Stat(filepath.Join(outside, "copy.txt"))
		assert.True(t, errors.Is(statErr, os.ErrNotExist))
	})

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := replaceRootWithSymlink(t, b)
		require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))

		var (
			objects []storage.ObjectInfo
			errs    []error
		)
		for obj, err := range b.List(ctx, "", storage.ListOptions{}) {
			if err != nil {
				errs = append(errs, err)
				continue
			}
			objects = append(objects, obj)
		}
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "symlink")
		assert.Empty(t, objects)
	})
}

func newBackend(t *testing.T, opts ...Option) *LocalBackend {
	t.Helper()
	b, err := New(t.TempDir(), opts...)
	require.NoError(t, err)
	return b
}

type trackingReadCloser struct {
	io.Reader
	closed *bool
}

func (r *trackingReadCloser) Close() error {
	*r.closed = true
	return nil
}

func replaceRootWithSymlink(t *testing.T, b *LocalBackend) string {
	t.Helper()
	outside := t.TempDir()
	require.NoError(t, os.RemoveAll(b.root))
	if err := os.Symlink(outside, b.root); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	return outside
}
