package localbackend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// collectList drains a List iterator, returning the matched keys and the
// first error encountered (if any).
func collectList(t *testing.T, b *Backend, ctx context.Context, prefix string, opts storage.ListOptions) ([]string, error) {
	t.Helper()
	var keys []string
	for obj, err := range b.List(ctx, prefix, opts) {
		if err != nil {
			return keys, err
		}
		keys = append(keys, obj.Key)
	}
	return keys, nil
}

// TestList_PrefixIsStringNotDirectory pins finding #1: List must treat prefix
// as a string prefix, matching membackend and S3, not as a directory path.
func TestList_PrefixIsStringNotDirectory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("partial directory-name prefix matches keys under it", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		require.NoError(t, b.Put(ctx, "logs/2026-06-01/a.log", bytes.NewReader([]byte("a")), storage.ObjectMeta{}))
		require.NoError(t, b.Put(ctx, "logs/2026-06-02/b.log", bytes.NewReader([]byte("b")), storage.ObjectMeta{}))
		require.NoError(t, b.Put(ctx, "logs/2026-07-01/c.log", bytes.NewReader([]byte("c")), storage.ObjectMeta{}))

		keys, err := collectList(t, b, ctx, "logs/2026-06-", storage.ListOptions{})
		require.NoError(t, err)
		sort.Strings(keys)
		assert.Equal(t, []string{"logs/2026-06-01/a.log", "logs/2026-06-02/b.log"}, keys)
	})

	t.Run("non-directory-aligned prefix matches sibling files", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		require.NoError(t, b.Put(ctx, "foo", bytes.NewReader([]byte("f")), storage.ObjectMeta{}))
		require.NoError(t, b.Put(ctx, "foobar.txt", bytes.NewReader([]byte("fb")), storage.ObjectMeta{}))
		require.NoError(t, b.Put(ctx, "baz.txt", bytes.NewReader([]byte("z")), storage.ObjectMeta{}))

		keys, err := collectList(t, b, ctx, "foo", storage.ListOptions{})
		require.NoError(t, err)
		sort.Strings(keys)
		assert.Equal(t, []string{"foo", "foobar.txt"}, keys)
	})
}

// TestList_LexicographicKeyOrder pins finding #2: keys must be yielded in
// lexicographic order so StartAfter pagination cannot skip objects. With keys
// "foo.txt" and "foo/bar", WalkDir tree order yields "foo/bar" before
// "foo.txt" ('.' < '/'), but lexicographically "foo.txt" < "foo/bar".
func TestList_LexicographicKeyOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := newBackend(t)

	require.NoError(t, b.Put(ctx, "foo.txt", bytes.NewReader([]byte("a")), storage.ObjectMeta{}))
	require.NoError(t, b.Put(ctx, "foo/bar", bytes.NewReader([]byte("b")), storage.ObjectMeta{}))

	keys, err := collectList(t, b, ctx, "", storage.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"foo.txt", "foo/bar"}, keys, "keys must be yielded in lexicographic order")
}

// TestList_StartAfterPaginationCompletes pins finding #2's downstream effect:
// keyset pagination via ListPage must visit every object exactly once.
func TestList_StartAfterPaginationCompletes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := newBackend(t)

	want := []string{"foo.txt", "foo/bar"}
	for _, k := range want {
		require.NoError(t, b.Put(ctx, k, bytes.NewReader([]byte("x")), storage.ObjectMeta{}))
	}

	var got []string
	startAfter := ""
	for {
		page, err := storage.ListPage(ctx, b, "", storage.ListOptions{MaxKeys: 1, StartAfter: startAfter})
		require.NoError(t, err)
		for _, obj := range page.Objects {
			got = append(got, obj.Key)
		}
		if !page.Truncated {
			break
		}
		startAfter = page.NextStartAfter
	}

	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got, "keyset pagination must visit every object exactly once")
}

// TestList_MidWalkCancellationYieldsError pins finding #3: a ctx cancelled
// mid-listing must surface ctx.Err(), not silently end with a truncated
// complete-looking result (matching membackend).
func TestList_MidWalkCancellationYieldsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBackend(t)

	for _, k := range []string{"a.txt", "b.txt", "c.txt"} {
		require.NoError(t, b.Put(context.Background(), k, bytes.NewReader([]byte("x")), storage.ObjectMeta{}))
	}

	var sawErr error
	seen := 0
	for _, err := range b.List(ctx, "", storage.ListOptions{}) {
		if err != nil {
			sawErr = err
			break
		}
		seen++
		// Cancel after the first successful item, mid-listing.
		cancel()
	}
	require.ErrorIs(t, sawErr, context.Canceled, "mid-walk cancellation must surface ctx.Err()")
	assert.Positive(t, seen, "expected at least one item before cancellation, so this exercises the mid-listing path")
}

// TestList_SkipsTempFiles pins finding #4: in-flight ".tmp-*" temp files (and
// crash-orphaned ones) must not appear as objects in List.
func TestList_SkipsTempFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := newBackend(t)

	require.NoError(t, b.Put(ctx, "real.txt", bytes.NewReader([]byte("r")), storage.ObjectMeta{}))
	require.NoError(t, b.Put(ctx, "a/real.txt", bytes.NewReader([]byte("r")), storage.ObjectMeta{}))

	// Simulate a crash-orphaned temp file left behind by a previous Put.
	require.NoError(t, os.WriteFile(filepath.Join(b.root, ".tmp-123456"), []byte("orphan"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(b.root, "a", ".tmp-789"), []byte("orphan"), 0o600))

	keys, err := collectList(t, b, ctx, "", storage.ListOptions{})
	require.NoError(t, err)
	sort.Strings(keys)
	assert.Equal(t, []string{"a/real.txt", "real.txt"}, keys)
	for _, k := range keys {
		assert.False(t, strings.Contains(k, ".tmp-"), "temp file leaked into List: %q", k)
	}
}

// TestGetExists_RejectImplicitDirectoryKeys pins finding #6: an implicit
// directory key (created by Put-ing a child) must not be readable. membackend
// and S3 return false / ErrObjectNotFound for such keys.
func TestGetExists_RejectImplicitDirectoryKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := newBackend(t)

	require.NoError(t, b.Put(ctx, "a/b", bytes.NewReader([]byte("child")), storage.ObjectMeta{}))

	t.Run("exists returns false for directory key", func(t *testing.T) {
		exists, err := b.Exists(ctx, "a")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("get returns ErrObjectNotFound for directory key", func(t *testing.T) {
		rc, _, err := b.Get(ctx, "a")
		if rc != nil {
			_ = rc.Close()
		}
		require.Error(t, err)
		assert.ErrorIs(t, err, storage.ErrObjectNotFound)
	})

	t.Run("copy returns ErrObjectNotFound for directory source key", func(t *testing.T) {
		err := b.Copy(ctx, "a", "dst.txt")
		require.Error(t, err)
		assert.ErrorIs(t, err, storage.ErrObjectNotFound)
	})
}

// TestCopy_ENOSPCMapping pins finding #7: the Copy write/sync error mapping
// must translate a wrapped syscall.ENOSPC to the kit capacity sentinel (the
// shared helper used by the production Copy path), so apperror.IsStorageFull
// recognises a disk-full Copy as a retryable capacity failure — matching Put.
func TestCopy_ENOSPCMapping(t *testing.T) {
	t.Parallel()

	enospc := &pathErr{op: "write", path: "/tmp", err: syscall.ENOSPC}
	mapped := copyFileError("copy write", enospc)

	require.ErrorIs(t, mapped, storage.ErrInsufficientCapacity)
	require.ErrorIs(t, mapped, syscall.ENOSPC)
	require.True(t, apperror.IsStorageFull(mapped))

	// A non-ENOSPC error must fall through to the redacted localFileError path.
	notExist := &pathErr{op: "open", path: "/tmp", err: os.ErrNotExist}
	require.ErrorIs(t, copyFileError("copy write", notExist), os.ErrNotExist)
	require.False(t, apperror.IsStorageFull(copyFileError("copy write", notExist)))
}
