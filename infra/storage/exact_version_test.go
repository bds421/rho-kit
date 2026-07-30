package storage_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/circuitbreaker"
	"github.com/bds421/rho-kit/infra/v2/storage/encryption"
	"github.com/bds421/rho-kit/infra/v2/storage/membackend"
	"github.com/bds421/rho-kit/infra/v2/storage/retry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObjectVersionValidate(t *testing.T) {
	t.Parallel()

	require.NoError(t, (storage.ObjectVersion{
		Key: "tenant/object", Version: "2026-07-27T12:34:56.1234567Z",
	}).Validate())

	for _, value := range []storage.ObjectVersion{
		{},
		{Key: "tenant/object"},
		{Key: "../object", Version: "v1"},
		{Key: "tenant/object", Version: "bad version"},
		{Key: "tenant/object", Version: "v1\n"},
	} {
		assert.ErrorIs(t, value.Validate(), storage.ErrValidation)
	}
}

func TestAsExactVersionStoreHonorsOpaqueDecorator(t *testing.T) {
	t.Parallel()

	versioned := exactVersionStub{}
	resolved, ok := storage.AsExactVersionStore(transparentExactWrapper{Storage: versioned})
	require.True(t, ok)
	assert.Equal(t, "v1", currentVersion(t, resolved).Version)

	_, ok = storage.AsExactVersionStore(opaqueExactWrapper{Storage: versioned})
	assert.False(t, ok)
}

func TestAsExactVersionPageListerHonorsOpaqueDecorator(t *testing.T) {
	t.Parallel()

	versioned := exactVersionStub{}
	resolved, ok := storage.AsExactVersionPageLister(
		transparentExactWrapper{Storage: versioned},
	)
	require.True(t, ok)
	page, err := resolved.VersionsPage(
		context.Background(),
		"",
		storage.ExactVersionPageOptions{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, page.Objects, 1)

	_, ok = storage.AsExactVersionPageLister(
		opaqueExactWrapper{Storage: versioned},
	)
	assert.False(t, ok)
}

func TestValidateExactVersionListLimit(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{1, storage.MaxExactVersionListEntries} {
		require.NoError(t, storage.ValidateExactVersionListLimit(limit))
	}
	for _, limit := range []int{0, -1, storage.MaxExactVersionListEntries + 1} {
		require.ErrorIs(
			t,
			storage.ValidateExactVersionListLimit(limit),
			storage.ErrBatchTooLarge,
		)
	}
}

func TestValidateExactVersionPageOptions(t *testing.T) {
	t.Parallel()

	require.NoError(t, storage.ValidateExactVersionPageOptions(
		storage.ExactVersionPageOptions{
			Limit:  storage.MaxExactVersionListEntries,
			Cursor: "opaque-token",
		},
	))
	for _, options := range []storage.ExactVersionPageOptions{
		{},
		{Limit: storage.MaxExactVersionListEntries + 1},
		{Limit: 1, Cursor: "bad cursor"},
		{Limit: 1, Cursor: "bad\ncursor"},
	} {
		require.Error(t, storage.ValidateExactVersionPageOptions(options))
	}
}

func TestExactVersionPageListerSurvivesSemanticDecoratorStack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	backend := membackend.NewImmutable()
	encrypted := encryption.New(
		backend,
		encryption.StaticKey(bytes.Repeat([]byte{7}, 32)),
	)
	require.NoError(t, encrypted.Put(
		ctx,
		"objects/a",
		bytes.NewReader([]byte("body")),
		storage.ObjectMeta{Size: 4},
	))
	wrapped := retry.New(circuitbreaker.New(encrypted))

	pager, ok := storage.AsExactVersionPageLister(wrapped)
	require.True(t, ok)
	page, err := pager.VersionsPage(
		ctx,
		"objects/",
		storage.ExactVersionPageOptions{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, page.Objects, 1)
	assert.Equal(t, "objects/a", page.Objects[0].Key)
	assert.False(t, page.Truncated)
	assert.Empty(t, page.NextCursor)

	unversioned := retry.New(circuitbreaker.New(encryption.New(
		membackend.New(),
		encryption.StaticKey(bytes.Repeat([]byte{9}, 32)),
	)))
	_, ok = storage.AsExactVersionPageLister(unversioned)
	assert.False(t, ok)
}

type exactVersionStub struct{}

func (exactVersionStub) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return nil
}
func (exactVersionStub) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	return nil, storage.ObjectMeta{}, nil
}
func (exactVersionStub) Delete(context.Context, string) error         { return nil }
func (exactVersionStub) Exists(context.Context, string) (bool, error) { return false, nil }
func (exactVersionStub) CurrentVersion(context.Context, string) (storage.ObjectVersion, storage.ObjectMeta, error) {
	return storage.ObjectVersion{Key: "key", Version: "v1"}, storage.ObjectMeta{}, nil
}
func (exactVersionStub) StatVersion(context.Context, storage.ObjectVersion) (storage.ObjectMeta, error) {
	return storage.ObjectMeta{}, nil
}
func (exactVersionStub) GetVersion(context.Context, storage.ObjectVersion) (io.ReadCloser, storage.ObjectMeta, error) {
	return nil, storage.ObjectMeta{}, nil
}
func (exactVersionStub) DeleteVersion(context.Context, storage.ObjectVersion) error { return nil }
func (exactVersionStub) VersionsPage(
	context.Context,
	string,
	storage.ExactVersionPageOptions,
) (storage.ExactVersionPage, error) {
	return storage.ExactVersionPage{
		Objects: []storage.ObjectVersion{{Key: "key", Version: "v1"}},
	}, nil
}

type transparentExactWrapper struct{ storage.Storage }

func (value transparentExactWrapper) Unwrap() storage.Storage { return value.Storage }

type opaqueExactWrapper struct{ storage.Storage }

func (value opaqueExactWrapper) Unwrap() storage.Storage { return value.Storage }
func (opaqueExactWrapper) OpaqueStorageDecorator()       {}

func currentVersion(t *testing.T, store storage.ExactVersionStore) storage.ObjectVersion {
	t.Helper()
	value, _, err := store.CurrentVersion(context.Background(), "key")
	require.NoError(t, err)
	return value
}
