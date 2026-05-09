package asvs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScanImports_FindsKitImports pins the FR-007 behaviour: the
// trustworthy claim set comes from real Go import statements, not
// from hand-written `// asvs:` comments.
func TestScanImports_FindsKitImports(t *testing.T) {
	dir := t.TempDir()
	src := `package fake

import (
	"context"

	"github.com/bds421/rho-kit/httpx/v2/middleware/csrf"
	"github.com/bds421/rho-kit/crypto/v2/passhash"
)

var _ = context.Background
var _ = csrf.Middleware
var _ = passhash.Hash
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fake.go"), []byte(src), 0o644))

	report, err := ScanImports(dir)
	require.NoError(t, err)

	// Both registry entries must be discovered.
	assert.Len(t, report.Imports, 2)
	// V13.2.3 (csrf), V3.4.1 (csrf), V6.2.1 (passhash) at minimum.
	claimed := map[ID]bool{}
	for _, id := range report.Claimed {
		claimed[id] = true
	}
	assert.True(t, claimed["V13.2.3"], "csrf should claim V13.2.3")
	assert.True(t, claimed["V3.4.1"], "csrf should claim V3.4.1")
	assert.True(t, claimed["V6.2.1"], "passhash should claim V6.2.1")
}

// FR-007 regression: comments alone do NOT grant evidence. A file
// containing `// asvs: V99.99.99` with NO import resolves to zero
// import-derived claims.
func TestScanImports_IgnoresCommentClaimsWithoutImport(t *testing.T) {
	dir := t.TempDir()
	src := `package fake

// asvs: V13.2.3
// We claim CSRF protection but DO NOT import the middleware.

func handler() {}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fake.go"), []byte(src), 0o644))

	report, err := ScanImports(dir)
	require.NoError(t, err)
	assert.Empty(t, report.Imports, "no imports → no import claims")
	assert.Empty(t, report.Claimed)
}

// Builder import promotes evidence from capability to
// builder-enforced for V14.1.1 (production-safety validator).
func TestScanImports_BuilderEvidenceClass(t *testing.T) {
	dir := t.TempDir()
	src := `package fake

import _ "github.com/bds421/rho-kit/app/v2"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fake.go"), []byte(src), 0o644))

	report, err := ScanImports(dir)
	require.NoError(t, err)
	assert.Equal(t, EvidenceBuilderEnforced, report.EvidenceByControl["V14.1.1"])
}

// When two packages claim the same control with different evidence
// classes, the strongest one wins.
func TestScanImports_StrongestEvidenceWins(t *testing.T) {
	// Synthesize claims directly to avoid needing two real kit
	// packages claiming the same control with different classes.
	imports := []ImportClaim{
		{Claim: PackageClaim{Controls: []ID{"V14.1.1"}, Evidence: EvidenceCapability}},
		{Claim: PackageClaim{Controls: []ID{"V14.1.1"}, Evidence: EvidenceBuilderEnforced}},
	}
	report := buildImportReport(imports)
	assert.Equal(t, EvidenceBuilderEnforced, report.EvidenceByControl["V14.1.1"])
}

// Vendor and testdata directories are skipped — we only want the
// service's own imports, not its dependencies' imports.
func TestScanImports_SkipsVendorAndTestdata(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"vendor", "testdata"} {
		sub := filepath.Join(dir, name)
		require.NoError(t, os.Mkdir(sub, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(sub, "x.go"),
			[]byte(`package x; import _ "github.com/bds421/rho-kit/crypto/v2/passhash"`),
			0o644))
	}

	report, err := ScanImports(dir)
	require.NoError(t, err)
	assert.Empty(t, report.Imports)
}

// Hand-curated registry sanity check: every catalog control referenced
// by PackageRegistry must resolve to a real Catalog entry. A typo in
// the registry would otherwise quietly produce reports that reference
// IDs nobody can look up.
func TestPackageRegistry_AllControlsAreInCatalog(t *testing.T) {
	for _, c := range PackageRegistry {
		for _, id := range c.Controls {
			_, err := Lookup(id)
			assert.NoError(t, err, "registry entry %s claims unknown control %s", c.ImportPath, id)
		}
	}
}

// Every registry import path is unique — duplicates would silently
// shadow each other.
func TestPackageRegistry_NoDuplicateImportPaths(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range PackageRegistry {
		assert.False(t, seen[c.ImportPath], "duplicate registry entry: %s", c.ImportPath)
		seen[c.ImportPath] = true
	}
}
