package contextutil_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/contextutil"
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

func TestRequestID_NilContext(t *testing.T) {
	//nolint:staticcheck // Deliberately exercises the nil-safe read path.
	got := contextutil.RequestID(nil)
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

// TestSetRequestID_RejectsInvalid asserts the security-relevant drop paths:
// invalid IDs must leave the context unchanged so a hostile inbound header
// cannot influence log lines or metric labels. A regression in the validation
// (e.g. dropping the length cap or the control-byte check) must fail here.
func TestSetRequestID_RejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"too long", strings.Repeat("a", contextutil.MaxRequestIDLen+1)},
		{"newline", "req\nid"},
		{"carriage return", "req\rid"},
		{"tab", "req\tid"},
		{"null byte", "req\x00id"},
		{"control byte", "req\x1fid"},
		{"del byte", "req\x7fid"},
		{"space", "req id"},
		{"high byte", "req\x80id"},
		{"non-ascii unicode", "req-é"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Seed a known-good value so a buggy setter that *stores* the
			// invalid input would be caught instead of silently passing.
			ctx := contextutil.SetRequestID(context.Background(), "seed")
			got := contextutil.SetRequestID(ctx, tt.id)
			assert.Equal(t, "seed", contextutil.RequestID(got),
				"invalid ID %q must be dropped, leaving the prior value intact", tt.id)
		})
	}
}

// TestSetRequestID_AcceptsValidEdges asserts the setter accepts the looser
// printable-ASCII baseline (a deliberate superset of IsValidCorrelationToken)
// and the exact length boundary.
func TestSetRequestID_AcceptsValidEdges(t *testing.T) {
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
			ctx := contextutil.SetRequestID(context.Background(), tt.id)
			assert.Equal(t, tt.id, contextutil.RequestID(ctx))
		})
	}
}
