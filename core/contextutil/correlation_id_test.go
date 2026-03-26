package contextutil_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/contextutil"
)

func TestSetCorrelationID_RoundTrip(t *testing.T) {
	ctx := contextutil.SetCorrelationID(context.Background(), "corr-xyz-789")

	got := contextutil.CorrelationID(ctx)
	require.Equal(t, "corr-xyz-789", got)
}

func TestCorrelationID_EmptyContext(t *testing.T) {
	got := contextutil.CorrelationID(context.Background())
	assert.Empty(t, got)
}

func TestSetCorrelationID_Overwrites(t *testing.T) {
	ctx := contextutil.SetCorrelationID(context.Background(), "first")
	ctx = contextutil.SetCorrelationID(ctx, "second")

	assert.Equal(t, "second", contextutil.CorrelationID(ctx))
}

func TestSetCorrelationID_DoesNotMutateParent(t *testing.T) {
	parent := contextutil.SetCorrelationID(context.Background(), "parent-id")
	_ = contextutil.SetCorrelationID(parent, "child-id")

	assert.Equal(t, "parent-id", contextutil.CorrelationID(parent))
}

func TestRequestIDAndCorrelationID_Independent(t *testing.T) {
	ctx := contextutil.SetRequestID(context.Background(), "req-1")
	ctx = contextutil.SetCorrelationID(ctx, "corr-1")

	assert.Equal(t, "req-1", contextutil.RequestID(ctx))
	assert.Equal(t, "corr-1", contextutil.CorrelationID(ctx))
}
