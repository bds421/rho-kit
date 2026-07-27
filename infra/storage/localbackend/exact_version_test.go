package localbackend

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImmutableBackendDeletesOnlyExactVersion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	backend, err := NewImmutable(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, backend.Put(ctx, "objects/a", bytes.NewBufferString("first"), storage.ObjectMeta{}))

	first, meta, err := backend.CurrentVersion(ctx, "objects/a")
	require.NoError(t, err)
	assert.Equal(t, int64(5), meta.Size)
	versions, err := backend.Versions(ctx, "objects/a", 2)
	require.NoError(t, err)
	require.Equal(t, []storage.ObjectVersion{first}, versions)
	require.ErrorIs(t,
		backend.Put(ctx, "objects/a", bytes.NewBufferString("second"), storage.ObjectMeta{}),
		storage.ErrValidation,
	)

	require.NoError(t, backend.DeleteVersion(ctx, storage.ObjectVersion{
		Key: "objects/a", Version: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}))
	body, _, err := backend.GetVersion(ctx, first)
	require.NoError(t, err)
	got, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	assert.Equal(t, "first", string(got))

	require.NoError(t, backend.DeleteVersion(ctx, first))
	versions, err = backend.Versions(ctx, "objects/a", 2)
	require.NoError(t, err)
	require.Empty(t, versions)
	_, _, err = backend.GetVersion(ctx, first)
	assert.ErrorIs(t, err, storage.ErrObjectNotFound)

	require.NoError(t, backend.Put(ctx, "objects/a", bytes.NewBufferString("first"), storage.ObjectMeta{}))
	second, _, err := backend.CurrentVersion(ctx, "objects/a")
	require.NoError(t, err)
	require.NotEqual(t, first.Version, second.Version,
		"recreating identical bytes must still create a distinct generation")
	require.NoError(t, backend.DeleteVersion(ctx, first))
	body, _, err = backend.GetVersion(ctx, second)
	require.NoError(t, err)
	require.NoError(t, body.Close())
}

func TestImmutableBackendPersistsGenerationAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	firstBackend, err := NewImmutable(root)
	require.NoError(t, err)
	require.NoError(t, firstBackend.Put(
		ctx,
		"objects/a",
		bytes.NewBufferString("body"),
		storage.ObjectMeta{},
	))
	first, _, err := firstBackend.CurrentVersion(ctx, "objects/a")
	require.NoError(t, err)
	require.NoError(t, firstBackend.Close())

	reopened, err := NewImmutable(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	current, _, err := reopened.CurrentVersion(ctx, "objects/a")
	require.NoError(t, err)
	assert.Equal(t, first, current)
}

func TestImmutableBackendVersionsByPrefixIsSortedAndBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend, err := NewImmutable(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	for _, key := range []string{"operations/a/z", "outside/a", "operations/a/a"} {
		require.NoError(t, backend.Put(
			ctx, key, bytes.NewBufferString(key), storage.ObjectMeta{},
		))
	}
	versions, err := backend.VersionsByPrefix(ctx, "operations/a/", 2)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, "operations/a/a", versions[0].Key)
	assert.Equal(t, "operations/a/z", versions[1].Key)
	_, err = backend.VersionsByPrefix(ctx, "operations/a/", 1)
	assert.ErrorIs(t, err, storage.ErrBatchTooLarge)
}
