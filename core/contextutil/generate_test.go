// Package contextutil (not _test) to access unexported fallbackGenerate.
package contextutil

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateID_ValidUUIDv7(t *testing.T) {
	id := GenerateID()

	parsed, err := uuid.Parse(id)
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), parsed.Version())
}

func TestGenerateID_Unique(t *testing.T) {
	id1 := GenerateID()
	id2 := GenerateID()

	assert.NotEqual(t, id1, id2)
}

func TestGenerateID_Format(t *testing.T) {
	id := GenerateID()
	assert.Len(t, id, 36)
}

func TestFallbackGenerate_Format(t *testing.T) {
	id := fallbackGenerate()
	assert.Len(t, id, 36)
}

func TestFallbackGenerate_Unique(t *testing.T) {
	id1 := fallbackGenerate()
	id2 := fallbackGenerate()

	assert.NotEqual(t, id1, id2)
}

func TestFallbackGenerate_VersionAndVariant(t *testing.T) {
	id := fallbackGenerate()

	parsed, err := uuid.Parse(id)
	require.NoError(t, err, "fallback ID is not valid UUID")
	assert.Equal(t, uuid.Version(7), parsed.Version(), "version = %d, want 7", parsed.Version())
	assert.Equal(t, uuid.RFC4122, parsed.Variant(), "variant = %v, want RFC4122", parsed.Variant())
}
