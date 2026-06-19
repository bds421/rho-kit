package tenant

import (
	"context"
	"errors"
	"fmt"
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

func TestNewID_RejectionDoesNotEchoOffendingCharacter(t *testing.T) {
	cases := map[string]string{
		"whitespace": "secret-token tenant",
		"forbidden":  "secret-token/tenant",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewID(input)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalid)
			assert.NotContains(t, err.Error(), "secret-token")
			assert.NotContains(t, err.Error(), "/")
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
	assert.NotContains(t, err.Error(), "256")
	assert.NotContains(t, err.Error(), "257")
}

func TestNewID_RejectsOverlongIDs(t *testing.T) {
	overMax := strings.Repeat("a", MaxIDLen+1)
	_, err := NewID(overMax)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalid)
}

func TestIDFromTrusted_BypassesValidation(t *testing.T) {
	// The escape hatch must accept inputs NewID rejects — that's its
	// whole purpose. Documented use case: reading from a trusted DB
	// column populated before C-3 was fixed.
	id := IDFromTrusted("a:b")
	assert.Equal(t, ID("a:b"), id)
	assert.Equal(t, "a:b", id.String())
}

func TestMustNewID_PanicsOnInvalid(t *testing.T) {
	assert.Panics(t, func() { MustNewID("a:b") })
}

func TestMustNewID_PanicCarriesValidationReason(t *testing.T) {
	// The panic value must surface the underlying ValidateID reason so a
	// startup crash names the failed rule rather than a generic message.
	// The wrapped messages are redaction-safe (no input content), so the
	// offending value must not appear in the panic.
	r := func() (msg string) {
		defer func() {
			rec := recover()
			require.NotNil(t, rec, "MustNewID must panic on invalid input")
			msg = fmt.Sprintf("%v", rec)
		}()
		MustNewID("a:b")
		return ""
	}()

	assert.Contains(t, r, "MustNewID")
	assert.Contains(t, r, "forbidden byte")
	assert.NotContains(t, r, "a:b")
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
	ctx, err := WithID(context.Background(), id)
	require.NoError(t, err)
	got, ok := FromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, id, got)
}

func TestWithID_NilContextUsesBackground(t *testing.T) {
	//nolint:staticcheck // Deliberately verifies normalization of nil context inputs.
	ctx, err := WithID(nil, ID("acme"))
	require.NoError(t, err)
	require.NotNil(t, ctx)

	got, ok := FromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, ID("acme"), got)
}

func TestWithID_ZeroIDNotPropagated(t *testing.T) {
	// Storing the zero value should not appear as "present" — it would
	// flip into an empty-string scope on the consumer side, which is a
	// silent multi-tenant collision.
	ctx, err := WithID(context.Background(), ID(""))
	require.NoError(t, err)
	_, ok := FromContext(ctx)
	assert.False(t, ok)
}

func TestWithID_ZeroIDNilContextReturnsBackground(t *testing.T) {
	//nolint:staticcheck // Deliberately verifies normalization of nil context inputs.
	ctx, err := WithID(nil, ID(""))
	require.NoError(t, err)
	require.NotNil(t, ctx)

	_, ok := FromContext(ctx)
	assert.False(t, ok)
}

func TestWithID_RefusesDifferentExistingTenant(t *testing.T) {
	ctx, err := WithID(context.Background(), ID("acme"))
	require.NoError(t, err)

	next, err := WithID(ctx, ID("widgets"))
	assert.True(t, ctx == next)
	assert.ErrorIs(t, err, ErrAlreadySet)
	assert.NotContains(t, err.Error(), "acme")
	assert.NotContains(t, err.Error(), "widgets")

	got, err := Required(ctx)
	require.NoError(t, err)
	assert.Equal(t, ID("acme"), got)
}

func TestWithID_AllowsSameExistingTenant(t *testing.T) {
	ctx, err := WithID(context.Background(), ID("acme"))
	require.NoError(t, err)
	next, err := WithID(ctx, ID("acme"))
	require.NoError(t, err)

	assert.True(t, ctx == next)
}

func TestRequired_AbsentReturnsErrMissing(t *testing.T) {
	_, err := Required(context.Background())
	assert.True(t, errors.Is(err, ErrMissing))
}

func TestRequired_PresentReturnsID(t *testing.T) {
	ctx, err := WithID(context.Background(), ID("acme"))
	require.NoError(t, err)
	got, err := Required(ctx)
	require.NoError(t, err)
	assert.Equal(t, ID("acme"), got)
}

func TestKey_BuildsLengthPrefixedTenantKey(t *testing.T) {
	ctx, err := WithID(context.Background(), ID("acme"))
	require.NoError(t, err)

	key, err := Key(ctx, "profile", "user:123")
	require.NoError(t, err)
	assert.Equal(t, "tenant:4:acme:7:profile:8:user:123", key)
}

func TestKey_MissingTenantReturnsErrMissing(t *testing.T) {
	key, err := Key(context.Background(), "profile")
	assert.Empty(t, key)
	assert.ErrorIs(t, err, ErrMissing)
}

func TestKeyFor_RejectsInvalidParts(t *testing.T) {
	cases := map[string][]string{
		"no parts":     {},
		"empty":        {""},
		"newline":      {"bad\nkey"},
		"space":        {"bad key"},
		"tab":          {"bad\tkey"},
		"null":         {"bad\x00key"},
		"invalid utf8": {string([]byte{'b', 0xff})},
		"overlong":     {strings.Repeat("a", MaxKeyPartLen+1)},
	}
	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			key, err := KeyFor(ID("acme"), parts...)
			assert.Empty(t, key)
			assert.ErrorIs(t, err, ErrKeyInvalid)
			if name == "overlong" {
				assert.NotContains(t, err.Error(), "1024")
				assert.NotContains(t, err.Error(), "1025")
			}
		})
	}
}

func TestKeyFor_RejectsTooManyParts(t *testing.T) {
	// Each part is individually valid; only the part-count cap should
	// reject the call. Boundary: MaxKeyParts is accepted, +1 rejected.
	atMax := make([]string, MaxKeyParts)
	for i := range atMax {
		atMax[i] = "p"
	}
	_, err := KeyFor(ID("acme"), atMax...)
	require.NoError(t, err, "exactly MaxKeyParts parts must be accepted")

	overMax := make([]string, MaxKeyParts+1)
	for i := range overMax {
		overMax[i] = "p"
	}
	key, err := KeyFor(ID("acme"), overMax...)
	assert.Empty(t, key)
	assert.ErrorIs(t, err, ErrKeyInvalid)
	// Distinguish the part-count guard from the per-part / total-length
	// guards so a regression dropping it can't pass on a different reason.
	assert.Contains(t, err.Error(), "part count")
}

func TestKeyFor_RejectsAssembledKeyExceedingTotalLen(t *testing.T) {
	// Every part passes the per-part cap (MaxKeyPartLen) and the call
	// stays within MaxKeyParts, so only the assembled-length guard can
	// reject this. 16 maxed parts produce >16 KiB once length prefixes
	// are added, exceeding MaxKeyTotalLen.
	const numParts = 16
	require.LessOrEqual(t, numParts, MaxKeyParts, "test must not trip the part-count cap")
	maxPart := strings.Repeat("a", MaxKeyPartLen)
	parts := make([]string, numParts)
	for i := range parts {
		parts[i] = maxPart
	}
	key, err := KeyFor(ID("acme"), parts...)
	assert.Empty(t, key)
	assert.ErrorIs(t, err, ErrKeyInvalid)
	assert.Contains(t, err.Error(), "total length")
}

func TestKeyFor_RejectsInvalidUncheckedTenantID(t *testing.T) {
	cases := []ID{
		IDFromTrusted("tenant\nid"),
		IDFromTrusted("tenant id"),
		IDFromTrusted("tenant\tid"),
		IDFromTrusted("tenant\x00id"),
		IDFromTrusted(string([]byte{'t', 0xff})),
		IDFromTrusted(strings.Repeat("a", MaxKeyPartLen+1)),
	}
	for _, id := range cases {
		key, err := KeyFor(id, "profile")
		assert.Empty(t, key)
		assert.ErrorIs(t, err, ErrKeyInvalid)
	}
}

func TestKeyFor_ColonInputsDoNotCollide(t *testing.T) {
	// IDFromTrusted (not MustNewID) — wave 68 renamed the
	// validation-bypass helper so MustNewID matches Go's panic-on-
	// invalid convention.
	ab, err := KeyFor(IDFromTrusted("a:b"), "c")
	require.NoError(t, err)

	a, err := KeyFor(IDFromTrusted("a"), "b:c")
	require.NoError(t, err)

	assert.NotEqual(t, ab, a)
	assert.Equal(t, "tenant:3:a:b:1:c", ab)
	assert.Equal(t, "tenant:1:a:3:b:c", a)
}
