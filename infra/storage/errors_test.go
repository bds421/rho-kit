package storage

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageError_DoesNotReflectKey(t *testing.T) {
	t.Parallel()

	cause := errors.New("backend failed while reading secret-token")
	err := NewTransientError("get-secret-token", "tenant/secret-token/object.txt", cause)

	require.Error(t, err)
	assert.Equal(t, "get-secret-token", err.Op)
	assert.Equal(t, "tenant/secret-token/object.txt", err.Key)
	assert.True(t, errors.Is(err, cause))
	assert.True(t, IsTransient(err))
	assert.EqualError(t, err, "storage: operation failed")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "tenant/")
	assert.NotContains(t, err.Error(), "backend failed")
}

func TestWrapSafe_PreservesCauseWithoutRenderingIt(t *testing.T) {
	t.Parallel()

	cause := errors.New("backend failed for tenant/secret-token/object.txt")

	err := WrapSafe("storage: backend operation failed", cause)

	require.Error(t, err)
	assert.ErrorIs(t, err, cause)
	assert.EqualError(t, err, "storage: backend operation failed")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "backend failed")
}
