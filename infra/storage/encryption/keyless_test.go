package encryption

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/membackend"
)

func TestWithoutKey_ListsAndDeletesWithoutReading(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.New()
	keyed := New(backend, StaticKey(testKey(t)))
	require.NoError(t, keyed.Put(ctx, "files/a", strings.NewReader("plaintext"), storage.ObjectMeta{}))

	keyless := WithoutKey(backend)

	lister, ok := storage.AsLister(keyless)
	require.True(t, ok)
	var listed []storage.ObjectInfo
	for info, err := range lister.List(ctx, "files/", storage.ListOptions{}) {
		require.NoError(t, err)
		listed = append(listed, info)
	}
	require.Len(t, listed, 1)
	assert.Equal(t, "files/a", listed[0].Key)
	assert.Equal(t, int64(len("plaintext")), listed[0].Size)

	_, _, err := keyless.Get(ctx, "files/a")
	require.ErrorIs(t, err, ErrNoKey)
	err = keyless.Put(ctx, "files/b", strings.NewReader("x"), storage.ObjectMeta{})
	require.ErrorIs(t, err, ErrNoKey)
	copier, ok := storage.AsCopier(keyless)
	require.True(t, ok)
	require.ErrorIs(t, copier.Copy(ctx, "files/a", "files/b"), ErrNoKey)
	exists, err := backend.Exists(ctx, "files/b")
	require.NoError(t, err)
	assert.False(t, exists, "a refused write stores nothing")

	require.NoError(t, keyless.Delete(ctx, "files/a"))
	exists, err = keyed.Exists(ctx, "files/a")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestWithoutKey_ExactVersionDeletesButDoesNotRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.NewImmutable()
	require.NoError(t, New(backend, StaticKey(testKey(t))).
		Put(ctx, "files/a", strings.NewReader("plaintext"), storage.ObjectMeta{}))

	exact, ok := storage.AsExactVersionStore(WithoutKey(backend))
	require.True(t, ok)
	object, meta, err := exact.CurrentVersion(ctx, "files/a")
	require.NoError(t, err)
	assert.Equal(t, int64(len("plaintext")), meta.Size)

	_, _, err = exact.GetVersion(ctx, object)
	require.ErrorIs(t, err, ErrNoKey)

	require.NoError(t, exact.DeleteVersion(ctx, object))
	_, _, err = exact.GetVersion(ctx, object)
	require.ErrorIs(t, err, ErrNoKey, "refused before the backend is asked")
	exists, err := backend.Exists(ctx, "files/a")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestWithoutKey_PanicsOnNilBackend(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { WithoutKey(nil) })
}
