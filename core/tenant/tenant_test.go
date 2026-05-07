package tenant

import (
	"context"
	"errors"
	"strings"
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

func TestNewID_RejectsColon(t *testing.T) {
	// The cache / idempotency wrappers use ':' as a field separator.
	// Allowing it inside a tenant ID is the C-3 cross-tenant collision.
	_, err := NewID("a:b")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalid)
}

func TestNewID_RejectsControlChars(t *testing.T) {
	cases := map[string]string{
		"newline":         "tenant\nid",
		"carriage return": "tenant\rid",
		"tab":             "tenant\tid",
		"null":            "tenant\x00id",
		"slash":           "tenant/id",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewID(input)
			require.Error(t, err, "expected %q to be rejected", input)
			assert.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestNewID_RejectsWhitespace(t *testing.T) {
	cases := map[string]string{
		"leading space":    " acme",
		"trailing space":   "acme ",
		"embedded space":   "ac me",
		"embedded tab":     "ac\tme",
		"only whitespace":  "   ",
		"nbsp embedded":    "ac me",
		"unicode space":    "ac me",
		"leading newline":  "\nacme",
		"trailing newline": "acme\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewID(input)
			require.Error(t, err, "expected %q to be rejected", input)
			assert.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestNewID_AcceptsAlphanum(t *testing.T) {
	cases := []string{
		"acme",
		"acme-prod",
		"acme_prod",
		"acme.prod",
		"ACME123",
		"550e8400-e29b-41d4-a716-446655440000", // UUID v4
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			id, err := NewID(input)
			require.NoError(t, err)
			assert.Equal(t, ID(input), id)
		})
	}
}

func TestValidateID_ReportsAllRejections(t *testing.T) {
	// Hits every documented rejection class so the contract stays
	// machine-checked, not just docstring-asserted.
	cases := map[string]string{
		"empty":           "",
		"colon":           "a:b",
		"slash":           "a/b",
		"newline":         "a\nb",
		"carriage return": "a\rb",
		"tab":             "a\tb",
		"null byte":       "a\x00b",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateID(input)
			require.Error(t, err, "expected %q to fail validation", input)
			assert.ErrorIs(t, err, ErrInvalid)
		})
	}

	// Sanity: a valid ID passes.
	assert.NoError(t, ValidateID("acme"))
}

func TestValidateID_RejectsOverlongIDs(t *testing.T) {
	// Bound length so a malicious header can't drive cache-key, log,
	// or metric blow-up.
	atMax := strings.Repeat("a", MaxIDLen)
	require.NoError(t, ValidateID(atMax))

	overMax := strings.Repeat("a", MaxIDLen+1)
	err := ValidateID(overMax)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalid)
}

func TestNewID_RejectsOverlongIDs(t *testing.T) {
	overMax := strings.Repeat("a", MaxIDLen+1)
	_, err := NewID(overMax)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalid)
}

func TestNewIDUnchecked_BypassesValidation(t *testing.T) {
	// The escape hatch must accept inputs NewID rejects — that's its
	// whole purpose. Documented use case: reading from a trusted DB
	// column populated before C-3 was fixed.
	id := NewIDUnchecked("a:b")
	assert.Equal(t, ID("a:b"), id)
	assert.Equal(t, "a:b", id.String())
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
