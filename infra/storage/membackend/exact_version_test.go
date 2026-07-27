package membackend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImmutableBackendDeletesOnlyExactVersion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	backend := NewImmutable()
	require.NoError(t, backend.Put(ctx, "objects/a", bytes.NewBufferString("first"), storage.ObjectMeta{}))

	first, meta, err := backend.CurrentVersion(ctx, "objects/a")
	require.NoError(t, err)
	assert.Equal(t, int64(5), meta.Size)
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
	_, _, err = backend.GetVersion(ctx, first)
	assert.ErrorIs(t, err, storage.ErrObjectNotFound)
	require.NoError(t, backend.DeleteVersion(ctx, first), "exact delete must be idempotent")

	require.NoError(t, backend.Put(ctx, "objects/a", bytes.NewBufferString("first"), storage.ObjectMeta{}))
	second, _, err := backend.CurrentVersion(ctx, "objects/a")
	require.NoError(t, err)
	require.NotEqual(t, first.Version, second.Version,
		"recreating identical bytes must still create a distinct generation")
	require.NoError(t, backend.DeleteVersion(ctx, first), "old generation must already be absent")
	_, _, err = backend.GetVersion(ctx, second)
	assert.NoError(t, err, "deleting the old generation must preserve the newer one")
}

func TestImmutableBackendExactVersionValidationAndCancellation(t *testing.T) {
	t.Parallel()

	backend := NewImmutable()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := backend.CurrentVersion(ctx, "objects/a")
	assert.ErrorIs(t, err, context.Canceled)
	err = backend.DeleteVersion(context.Background(), storage.ObjectVersion{Key: "objects/a"})
	assert.True(t, errors.Is(err, storage.ErrValidation))
}
