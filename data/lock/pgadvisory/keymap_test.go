package pgadvisory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyToInt64_DeterministicAcrossCalls(t *testing.T) {
	a := keyToInt64("user-42")
	b := keyToInt64("user-42")
	assert.Equal(t, a, b, "same key must map to the same int64 across calls and processes")
}

func TestKeyToInt64_DistinctKeysProduceDistinctIDs(t *testing.T) {
	a := keyToInt64("user-42")
	b := keyToInt64("user-43")
	assert.NotEqual(t, a, b, "different keys should typically produce different IDs (SHA-256)")
}

// TestKeyToInt64_GoldenValues pins the SHA-256-derived advisory-lock id
// for known keys. These values are part of the cross-process contract:
// two processes that hash the same string MUST land on the same Postgres
// advisory-lock id, so a regression in the hashing (e.g. reverting to
// FNV-1a, or changing the byte slice / endianness) would silently break
// mutual exclusion. The constants below were captured from the SHA-256
// implementation; if this test fails, the hashing changed and every
// existing held lock would map to a different id.
func TestKeyToInt64_GoldenValues(t *testing.T) {
	cases := map[string]int64{
		// int64(BigEndian.Uint64(sha256(key)[:8]))
		"":        -2039914840885289964,
		"user-42": 7892921889885005129,
	}
	for key, want := range cases {
		got := keyToInt64(key)
		assert.Equalf(t, want, got, "keyToInt64(%q) golden mismatch — hashing contract changed", key)
	}
}

func TestKeyToInt64_HandlesEmptyString(t *testing.T) {
	// Empty string still hashes deterministically (keyToInt64 itself
	// does not validate — validation lives in Acquire/AcquireTx). Two
	// calls must agree so the hashing layer is stable for any input.
	a := keyToInt64("")
	b := keyToInt64("")
	assert.Equal(t, a, b, "empty string must hash deterministically")
}

func TestValidateLockKey(t *testing.T) {
	require.NoError(t, validateLockKey("user-42"), "ordinary key must pass")
	require.NoError(t, validateLockKey("a/b:c-1_2"), "punctuation in printable range must pass")

	bad := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"nul byte", "kit\x00key"},
		{"control byte", "kit\x01key"},
		{"newline", "kit\nkey"},
		{"del byte", "kit\x7fkey"},
		{"too long", longKey(MaxLockKeyLen + 1)},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, validateLockKey(tc.key), "invalid key must be rejected")
		})
	}

	require.NoError(t, validateLockKey(longKey(MaxLockKeyLen)), "key at the length cap must pass")
}

func longKey(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
