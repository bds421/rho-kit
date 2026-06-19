package contextutil_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/contextutil"
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

func TestCorrelationID_NilContext(t *testing.T) {
	//nolint:staticcheck // Deliberately exercises the nil-safe read path.
	got := contextutil.CorrelationID(nil)
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

// TestSetCorrelationID_RejectsInvalid mirrors the request-ID drop-path test:
// invalid IDs must leave the context unchanged so a hostile inbound header
// cannot influence log lines or metric labels.
func TestSetCorrelationID_RejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"too long", strings.Repeat("a", contextutil.MaxRequestIDLen+1)},
		{"newline", "corr\nid"},
		{"carriage return", "corr\rid"},
		{"tab", "corr\tid"},
		{"null byte", "corr\x00id"},
		{"control byte", "corr\x1fid"},
		{"del byte", "corr\x7fid"},
		{"space", "corr id"},
		{"high byte", "corr\x80id"},
		{"non-ascii unicode", "corr-é"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := contextutil.SetCorrelationID(context.Background(), "seed")
			got := contextutil.SetCorrelationID(ctx, tt.id)
			assert.Equal(t, "seed", contextutil.CorrelationID(got),
				"invalid ID %q must be dropped, leaving the prior value intact", tt.id)
		})
	}
}

// TestSetCorrelationID_AcceptsValidEdges asserts the setter accepts the looser
// printable-ASCII baseline and the exact length boundary.
func TestSetCorrelationID_AcceptsValidEdges(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"max length", strings.Repeat("a", contextutil.MaxRequestIDLen)},
		{"single char", "x"},
		{"punctuation superset", "token=secret;x{}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := contextutil.SetCorrelationID(context.Background(), tt.id)
			assert.Equal(t, tt.id, contextutil.CorrelationID(ctx))
		})
	}
}

func TestRequestIDAndCorrelationID_Independent(t *testing.T) {
	ctx := contextutil.SetRequestID(context.Background(), "req-1")
	ctx = contextutil.SetCorrelationID(ctx, "corr-1")

	assert.Equal(t, "req-1", contextutil.RequestID(ctx))
	assert.Equal(t, "corr-1", contextutil.CorrelationID(ctx))
}
