package asvs_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/security/v2/asvs"
)

func TestLookup_KnownID(t *testing.T) {
	c, err := asvs.Lookup("V2.1.5")
	require.NoError(t, err)
	assert.Equal(t, asvs.ID("V2.1.5"), c.ID)
	assert.Equal(t, "Authentication", c.Chapter)
}

func TestLookup_UnknownID(t *testing.T) {
	_, err := asvs.Lookup("V99.9.9")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "V99.9.9")
}

func TestIDs_SortedAndComplete(t *testing.T) {
	ids := asvs.IDs()
	require.NotEmpty(t, ids)
	for i := 1; i < len(ids); i++ {
		assert.LessOrEqual(t, string(ids[i-1]), string(ids[i]),
			"IDs() must return sorted output for stable kit-doctor rendering")
	}
}

func TestCatalog_ReturnsDetachedCopy(t *testing.T) {
	catalog := asvs.Catalog()
	require.NotEmpty(t, catalog)

	original := catalog[0]
	catalog[0].ID = "V99.9.9"

	fresh := asvs.Catalog()
	assert.Equal(t, original, fresh[0])
	_, err := asvs.Lookup(original.ID)
	require.NoError(t, err)
}

func TestParseAnnotation_Basic(t *testing.T) {
	got := asvs.ParseAnnotation("// asvs: V2.1.5, V3.4.1, V13.2.3")
	require.Len(t, got, 3)
	assert.Equal(t, asvs.ID("V2.1.5"), got[0])
	assert.Equal(t, asvs.ID("V3.4.1"), got[1])
	assert.Equal(t, asvs.ID("V13.2.3"), got[2])
}

func TestParseAnnotation_Empty(t *testing.T) {
	assert.Nil(t, asvs.ParseAnnotation("// no asvs marker here"))
	assert.Nil(t, asvs.ParseAnnotation("// asvs:"))
	assert.Nil(t, asvs.ParseAnnotation("// asvs:   "))
}

func TestParseAnnotation_BlockComment(t *testing.T) {
	got := asvs.ParseAnnotation("/* asvs: V9.1.1 */")
	require.Len(t, got, 1)
	assert.Equal(t, asvs.ID("V9.1.1"), got[0])
}

func TestParseAnnotation_TrimsWhitespace(t *testing.T) {
	got := asvs.ParseAnnotation("// asvs:  V2.1.5  ,V3.4.1  ")
	require.Len(t, got, 2)
	assert.Equal(t, asvs.ID("V2.1.5"), got[0])
	assert.Equal(t, asvs.ID("V3.4.1"), got[1])
}

func TestEveryCatalogEntry_LooksUp(t *testing.T) {
	for _, c := range asvs.Catalog() {
		_, err := asvs.Lookup(c.ID)
		require.NoErrorf(t, err, "Lookup(%q) failed", c.ID)
	}
}
