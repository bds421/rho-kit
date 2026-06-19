package id_test

import (
	"errors"
	"regexp"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/id"
)

// canonicalUUID matches the lower-case 8-4-4-4-12 form that
// google/uuid emits from String().
var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestNew_ReturnsCanonicalUUIDv7(t *testing.T) {
	s := id.New()

	require.Regexp(t, canonicalUUID, s,
		"New must emit a canonical 36-char UUID")

	parsed, err := uuid.Parse(s)
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), parsed.Version(),
		"New must mint UUID v7 specifically")
	assert.Equal(t, uuid.RFC4122, parsed.Variant(),
		"UUID v7 must carry the RFC 4122 variant bits")
}

func TestNew_GeneratesUniqueValues(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for range n {
		s := id.New()
		_, dup := seen[s]
		require.False(t, dup, "New must not repeat within %d calls", n)
		seen[s] = struct{}{}
	}
}

func TestNewBytes_ReturnsUUIDv7Bytes(t *testing.T) {
	b := id.NewBytes()

	parsed, err := uuid.FromBytes(b[:])
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), parsed.Version())
	assert.Equal(t, uuid.RFC4122, parsed.Variant())
}

func TestNewBytes_GeneratesUniqueValues(t *testing.T) {
	const n = 1000
	seen := make(map[[16]byte]struct{}, n)
	for range n {
		b := id.NewBytes()
		_, dup := seen[b]
		require.False(t, dup, "NewBytes must not repeat within %d calls", n)
		seen[b] = struct{}{}
	}
}

func TestNew_SafeForConcurrentUse(t *testing.T) {
	const goroutines = 16
	const perG = 256

	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[string]struct{}, goroutines*perG)

	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			local := make([]string, perG)
			for i := range local {
				local[i] = id.New()
			}
			mu.Lock()
			defer mu.Unlock()
			for _, s := range local {
				_, dup := seen[s]
				require.False(t, dup, "concurrent New must not collide")
				seen[s] = struct{}{}
			}
		}()
	}
	wg.Wait()
}

func TestParse_RoundTripsCanonicalString(t *testing.T) {
	s := id.New()

	b, err := id.Parse(s)
	require.NoError(t, err)

	parsed, err := uuid.FromBytes(b[:])
	require.NoError(t, err)
	assert.Equal(t, s, parsed.String(),
		"Parse must round-trip back to the original string")
}

func TestParse_RejectsMalformedInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"random text", "not-a-uuid"},
		{"wrong length", "abcd"},
		{"truncated", "00000000-0000-7000-8000-00000000000"},
		{"non-hex", "zzzzzzzz-zzzz-7zzz-8zzz-zzzzzzzzzzzz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := id.Parse(tc.in)
			require.Error(t, err)
			assert.True(t, apperror.IsValidation(err),
				"Parse must surface a kit ValidationError, got %T", err)
		})
	}
}

func TestParse_WrapsUnderlyingError(t *testing.T) {
	_, err := id.Parse("not-a-uuid")
	require.Error(t, err)

	// The underlying google/uuid error must remain reachable via
	// errors.Unwrap for callers that want to inspect the cause.
	cause := errors.Unwrap(err)
	require.NotNil(t, cause, "apperror.NewValidationWithCause must keep the cause unwrappable")
}

// TestGenerator_IsSwappableForDeterministicOutput demonstrates the
// test-mode swap that consumers should follow when they want stable
// IDs in log assertions or golden files.
//
// This test deliberately does NOT call t.Parallel: Generator is an
// unsynchronized package-level variable, so reassigning it while a
// parallel sibling reads it would be a data race (see the Generator
// doc comment). Keep all Generator-swap tests serial.
func TestGenerator_IsSwappableForDeterministicOutput(t *testing.T) {
	const fixedID = "01900000-0000-7000-8000-000000000001"

	prev := id.Generator
	t.Cleanup(func() { id.Generator = prev })

	id.Generator = func() string { return fixedID }

	assert.Equal(t, fixedID, id.Generator())
	assert.Equal(t, fixedID, id.Generator(), "swapped Generator must be stable across calls")

	// The default New continues to emit fresh values regardless of the
	// Generator swap — Generator is the variable; New is the function.
	assert.NotEqual(t, fixedID, id.New(),
		"swapping Generator must not affect direct New() callers")
}

func TestGenerator_DefaultsToNew(t *testing.T) {
	// Reading the default-installed Generator must produce canonical
	// v7 values. Use the variable indirectly so the test does not
	// accidentally depend on a previous test forgetting to restore it.
	s := id.Generator()
	require.Regexp(t, canonicalUUID, s)

	parsed, err := uuid.Parse(s)
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), parsed.Version())
}
