package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewID_RejectsEmpty(t *testing.T) {
	_, err := NewID("")
	assert.ErrorIs(t, err, ErrInvalid)
}

func TestNewID_AcceptsNonEmpty(t *testing.T) {
	id, err := NewID("acme")
	require.NoError(t, err)
	assert.Equal(t, ID("acme"), id)
	assert.False(t, id.IsZero())
	assert.Equal(t, "acme", id.String())
}

func TestFromContext_AbsentReturnsFalse(t *testing.T) {
	_, ok := FromContext(context.Background())
	assert.False(t, ok)
}

func TestFromContext_NilContextSafe(t *testing.T) {
	_, ok := FromContext(nil) //nolint:staticcheck // the helper must tolerate nil ctx
	assert.False(t, ok)
}

func TestWithID_RoundTrip(t *testing.T) {
	id := ID("acme")
	ctx := WithID(context.Background(), id)
	got, ok := FromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, id, got)
}

func TestWithID_ZeroIDNotPropagated(t *testing.T) {
	// Storing the zero value should not appear as "present" — it would
	// flip into an empty-string scope on the consumer side, which is a
	// silent multi-tenant collision.
	ctx := WithID(context.Background(), ID(""))
	_, ok := FromContext(ctx)
	assert.False(t, ok)
}

func TestRequired_AbsentReturnsErrMissing(t *testing.T) {
	_, err := Required(context.Background())
	assert.True(t, errors.Is(err, ErrMissing))
}

func TestRequired_PresentReturnsID(t *testing.T) {
	ctx := WithID(context.Background(), ID("acme"))
	got, err := Required(ctx)
	require.NoError(t, err)
	assert.Equal(t, ID("acme"), got)
}
