// Package contextutil (not _test) to access unexported fallbackGenerate.
package contextutil

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewID_ValidUUIDv7(t *testing.T) {
	id := NewID()

	parsed, err := uuid.Parse(id)
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), parsed.Version())
}

func TestNewID_Unique(t *testing.T) {
	id1 := NewID()
	id2 := NewID()

	assert.NotEqual(t, id1, id2)
}

func TestNewID_Format(t *testing.T) {
	id := NewID()
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
