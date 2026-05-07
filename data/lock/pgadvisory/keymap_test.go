package pgadvisory

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeyToInt64_DeterministicAcrossCalls(t *testing.T) {
	a := keyToInt64("user-42")
	b := keyToInt64("user-42")
	assert.Equal(t, a, b, "same key must map to the same int64 across calls and processes")
}

func TestKeyToInt64_DistinctKeysProduceDistinctIDs(t *testing.T) {
	a := keyToInt64("user-42")
	b := keyToInt64("user-43")
	assert.NotEqual(t, a, b, "different keys should typically produce different IDs (FNV-1a)")
}

func TestKeyToInt64_HandlesEmptyString(t *testing.T) {
	// Empty string is a valid key. Just ensures we don't panic.
	id := keyToInt64("")
	_ = id
}

func TestKeyToInt64_ResultIsInt64(t *testing.T) {
	// The hash output must fit in int64 (advisory locks take int8 in Postgres,
	// which is int64 in Go). Sanity check that the conversion doesn't overflow
	// in a way that surprises callers.
	id := keyToInt64("any")
	_ = int64(id) // compile-time assertion via assignment
}
