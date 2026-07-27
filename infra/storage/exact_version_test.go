package storage_test

import (
	"context"
	"io"
	"testing"

	"github.com/bds421/rho-kit/infra/v2/storage"
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
