package storagetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
)

// testPrefix returns a unique prefix for each test run to avoid collisions
// when multiple test runs share the same backend (e.g., shared S3 bucket).
func testPrefix(t *testing.T) string {
	t.Helper()
	// Use test name + crypto-random suffix to guarantee uniqueness across
	// parallel test runs sharing a backend.
	name := strings.ReplaceAll(t.Name(), "/", "-")
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return name + "-" + hex.EncodeToString(buf[:]) + "/"
}

// BackendSuite runs a standard compliance suite against any [storage.Storage]
// implementation. Call this from each backend's test to verify correct behavior:
//
//	func TestLocalBackendCompliance(t *testing.T) {
//	    backend := storagetest.NewLocalBackend(t)
//	    storagetest.BackendSuite(t, backend)
//	}
func BackendSuite(t *testing.T, backend storage.Storage) {
	t.Helper()
	ctx := context.Background()
	pfx := testPrefix(t)

	t.Run("PutAndGet", func(t *testing.T) {
		content := []byte("hello storage")
		key := pfx + "put-get.txt"
		meta := storage.ObjectMeta{ContentType: "text/plain"}

		err := backend.Put(ctx, key, bytes.NewReader(content), meta)
		require.NoError(t, err)

		rc, gotMeta, err := backend.Get(ctx, key)
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()

		got, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, content, got)
		assert.Greater(t, gotMeta.Size, int64(0))
	})

	t.Run("PutOverwrites", func(t *testing.T) {
		key := pfx + "overwrite.txt"

		err := backend.Put(ctx, key, bytes.NewReader([]byte("v1")), storage.ObjectMeta{})
		require.NoError(t, err)

		err = backend.Put(ctx, key, bytes.NewReader([]byte("v2")), storage.ObjectMeta{})
		require.NoError(t, err)

		rc, _, err := backend.Get(ctx, key)
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()

		got, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, []byte("v2"), got)
	})

	t.Run("GetNotFound", func(t *testing.T) {
		_, _, err := backend.Get(ctx, pfx+"does-not-exist")
		assert.ErrorIs(t, err, storage.ErrObjectNotFound)
	})

	t.Run("ExistsTrue", func(t *testing.T) {
		key := pfx + "exists-true.txt"
		err := backend.Put(ctx, key, bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.NoError(t, err)

		ok, err := backend.Exists(ctx, key)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("ExistsFalse", func(t *testing.T) {
		ok, err := backend.Exists(ctx, pfx+"does-not-exist")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("DeleteExisting", func(t *testing.T) {
		key := pfx + "delete-existing.txt"
		err := backend.Put(ctx, key, bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.NoError(t, err)

		require.NoError(t, backend.Delete(ctx, key))

		ok, err := backend.Exists(ctx, key)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("DeleteIdempotent", func(t *testing.T) {
		assert.NoError(t, backend.Delete(ctx, pfx+"never-existed"))
	})

	t.Run("EmptyKeyRejected", func(t *testing.T) {
		err := backend.Put(ctx, "", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		assert.Error(t, err)

		_, _, err = backend.Get(ctx, "")
		assert.Error(t, err)

		err = backend.Delete(ctx, "")
		assert.Error(t, err)

		_, err = backend.Exists(ctx, "")
		assert.Error(t, err)
	})

	t.Run("NestedKeys", func(t *testing.T) {
		key := pfx + "a/b/c/nested.txt"
		content := []byte("nested content")

		err := backend.Put(ctx, key, bytes.NewReader(content), storage.ObjectMeta{})
		require.NoError(t, err)

		rc, _, err := backend.Get(ctx, key)
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()

		got, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, content, got)
	})

	t.Run("ContentTypeRoundTrip", func(t *testing.T) {
		key := pfx + "content-type.json"
		content := []byte(`{"ok":true}`)
		meta := storage.ObjectMeta{ContentType: "application/json"}

		err := backend.Put(ctx, key, bytes.NewReader(content), meta)
		require.NoError(t, err)

		rc, gotMeta, err := backend.Get(ctx, key)
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()

		// Not all backends preserve ContentType (e.g. local filesystem).
		// Only assert when the backend returns a non-empty value.
		if gotMeta.ContentType != "" {
			assert.Equal(t, "application/json", gotMeta.ContentType,
				"ContentType should be preserved through Put/Get round trip")
		}
	})

	t.Run("LargeContent", func(t *testing.T) {
		key := pfx + "large.bin"
		// 1 MiB of data.
		content := bytes.Repeat([]byte("A"), 1<<20)

		err := backend.Put(ctx, key, bytes.NewReader(content), storage.ObjectMeta{})
		require.NoError(t, err)

		rc, meta, err := backend.Get(ctx, key)
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()

		got, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, content, got)
		assert.Equal(t, int64(1<<20), meta.Size)
	})
}

// ListerSuite runs compliance tests for backends implementing [storage.Lister].
// Requires that the backend also implements [storage.Storage] for seeding data.
//
//	func TestLocalBackendListerCompliance(t *testing.T) {
//	    backend := storagetest.NewLocalBackend(t)
//	    storagetest.ListerSuite(t, backend, backend)
//	}
func ListerSuite(t *testing.T, backend storage.Storage, lister storage.Lister) {
	t.Helper()
	ctx := context.Background()
	pfx := testPrefix(t)

	// Seed data for list tests.
	files := map[string][]byte{
		pfx + "list/a.txt":          []byte("a"),
		pfx + "list/b.txt":          []byte("bb"),
		pfx + "list/sub/c.txt":      []byte("ccc"),
		pfx + "list/sub/d.txt":      []byte("dddd"),
		pfx + "list/sub/deep/e.txt": []byte("eeeee"),
		pfx + "other/f.txt":         []byte("ffffff"),
	}
	for key, content := range files {
		require.NoError(t, backend.Put(ctx, key, bytes.NewReader(content), storage.ObjectMeta{}))
	}

	t.Run("ListAll", func(t *testing.T) {
		var keys []string
		for info, err := range lister.List(ctx, pfx, storage.ListOptions{}) {
			require.NoError(t, err)
			keys = append(keys, info.Key)
		}

		assert.GreaterOrEqual(t, len(keys), 6)
	})

	t.Run("ListWithPrefix", func(t *testing.T) {
		var keys []string
		for info, err := range lister.List(ctx, pfx+"list/sub/", storage.ListOptions{}) {
			require.NoError(t, err)
			keys = append(keys, info.Key)
		}

		sort.Strings(keys)
		assert.Contains(t, keys, pfx+"list/sub/c.txt")
		assert.Contains(t, keys, pfx+"list/sub/d.txt")
		assert.Contains(t, keys, pfx+"list/sub/deep/e.txt")
		assert.NotContains(t, keys, pfx+"list/a.txt")
		assert.NotContains(t, keys, pfx+"other/f.txt")
	})

	t.Run("ListMaxKeys", func(t *testing.T) {
		var keys []string
		for info, err := range lister.List(ctx, pfx+"list/", storage.ListOptions{MaxKeys: 2}) {
			require.NoError(t, err)
			keys = append(keys, info.Key)
		}

		assert.Len(t, keys, 2)
	})

	t.Run("ListEmptyPrefix", func(t *testing.T) {
		var keys []string
		for info, err := range lister.List(ctx, pfx+"nonexistent/", storage.ListOptions{}) {
			require.NoError(t, err)
			keys = append(keys, info.Key)
		}

		assert.Empty(t, keys)
	})

	t.Run("ListPopulatesSize", func(t *testing.T) {
		for info, err := range lister.List(ctx, pfx+"list/a.txt", storage.ListOptions{MaxKeys: 1}) {
			require.NoError(t, err)
			assert.Equal(t, int64(1), info.Size)
			break
		}
	})

	t.Run("ListStartAfter", func(t *testing.T) {
		// Collect all keys sorted to determine the cursor.
		var allKeys []string
		for info, err := range lister.List(ctx, pfx+"list/", storage.ListOptions{}) {
			require.NoError(t, err)
			allKeys = append(allKeys, info.Key)
		}
		sort.Strings(allKeys)
		require.GreaterOrEqual(t, len(allKeys), 3, "need at least 3 keys for StartAfter test")

		// Use the second key as the cursor — expect everything after it.
		cursor := allKeys[1]
		var afterKeys []string
		for info, err := range lister.List(ctx, pfx+"list/", storage.ListOptions{StartAfter: cursor}) {
			require.NoError(t, err)
			afterKeys = append(afterKeys, info.Key)
		}

		sort.Strings(afterKeys)
		assert.NotContains(t, afterKeys, allKeys[0], "first key should be excluded")
		assert.NotContains(t, afterKeys, cursor, "cursor key itself should be excluded")
		assert.Equal(t, allKeys[2:], afterKeys, "should return all keys after cursor")
	})
}
