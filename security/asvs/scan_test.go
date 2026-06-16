package asvs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/security/v2/asvs"
)

func TestScanDir_FindsAnnotations(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte(`// asvs: V2.1.5, V13.2.3
package a
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.go"), []byte(`package b

// asvs: V9.1.1
func X() {}
`), 0o644))

	got, err := asvs.ScanDir(dir)
	require.NoError(t, err)
	require.Len(t, got.Annotations, 3)
	assert.Equal(t, []asvs.ID{"V13.2.3", "V2.1.5", "V9.1.1"}, got.Claimed)
}

func TestScanDir_DotRootScansCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte(`// asvs: V2.1.5
package a
`), 0o644))
	t.Chdir(dir)

	got, err := asvs.ScanDir(".")
	require.NoError(t, err)
	assert.Equal(t, []asvs.ID{"V2.1.5"}, got.Claimed)
}

func TestScanDir_SkipsSymlinkedGoFiles(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "outside.go")
	require.NoError(t, os.WriteFile(target, []byte(`// asvs: V2.1.5
package outside
`), 0o644))
	if err := os.Symlink(target, filepath.Join(dir, "linked.go")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	got, err := asvs.ScanDir(dir)
	require.NoError(t, err)
	assert.Empty(t, got.Claimed, "ScanDir must not claim controls from symlinked files outside root")
}

func TestScanDir_SkipsTestFilesAndVendor(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "live.go"), []byte(`// asvs: V2.1.5
package live
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "live_test.go"), []byte(`// asvs: V99.0.0
package live
`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "vendor", "x"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vendor", "x", "v.go"), []byte(`// asvs: V77.0.0
package x
`), 0o644))

	got, err := asvs.ScanDir(dir)
	require.NoError(t, err)
	assert.Equal(t, []asvs.ID{"V2.1.5"}, got.Claimed)
}

// testdata directories are skipped — annotations in Go fixtures under
// testdata/ (golden files, example sources) are documentation for tests,
// not controls the service actually claims on the request path. This
// matches the sibling ScanImports, which also skips testdata.
func TestScanDir_SkipsTestdata(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "live.go"), []byte(`// asvs: V2.1.5
package live
`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "testdata", "fixtures"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "testdata", "fixtures", "golden.go"), []byte(`// asvs: V77.0.0
package fixtures
`), 0o644))

	got, err := asvs.ScanDir(dir)
	require.NoError(t, err)
	assert.Equal(t, []asvs.ID{"V2.1.5"}, got.Claimed,
		"ScanDir must not harvest annotations from testdata fixtures")
}

func TestScanDir_FlagsUnknownIDs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte(`// asvs: V99.99.99
package a
`), 0o644))

	got, err := asvs.ScanDir(dir)
	require.NoError(t, err)
	assert.Equal(t, []asvs.ID{"V99.99.99"}, got.Unknown,
		"unknown IDs must surface for typo detection")
}

func TestScanDir_ReportsMissingFromCatalog(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte(`// asvs: V2.1.5
package a
`), 0o644))

	got, err := asvs.ScanDir(dir)
	require.NoError(t, err)
	assert.NotEmpty(t, got.Missing,
		"a tiny scan must report many catalog IDs as missing")
	for _, id := range got.Missing {
		assert.NotEqual(t, asvs.ID("V2.1.5"), id, "claimed ID must not appear in Missing")
	}
}

func TestScanDir_FilesystemErrorDoesNotReflectRootPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secret-token-root")

	_, err := asvs.ScanDir(root)
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Contains(t, err.Error(), "asvs: walk source tree")
	assert.NotContains(t, err.Error(), root)
	assert.NotContains(t, err.Error(), "secret-token-root")
	assert.True(t, errors.Is(err, os.ErrNotExist))
}
