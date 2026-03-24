package contextutil_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/contextutil"
)

func TestSetRequestID_RoundTrip(t *testing.T) {
	ctx := contextutil.SetRequestID(context.Background(), "req-abc-123")

	got := contextutil.RequestID(ctx)
	require.Equal(t, "req-abc-123", got)
}

func TestRequestID_EmptyContext(t *testing.T) {
	got := contextutil.RequestID(context.Background())
	assert.Empty(t, got)
}

func TestSetRequestID_Overwrites(t *testing.T) {
	ctx := contextutil.SetRequestID(context.Background(), "first")
	ctx = contextutil.SetRequestID(ctx, "second")

	assert.Equal(t, "second", contextutil.RequestID(ctx))
}

func TestSetRequestID_DoesNotMutateParent(t *testing.T) {
	parent := contextutil.SetRequestID(context.Background(), "parent-id")
	_ = contextutil.SetRequestID(parent, "child-id")

	assert.Equal(t, "parent-id", contextutil.RequestID(parent))
}
