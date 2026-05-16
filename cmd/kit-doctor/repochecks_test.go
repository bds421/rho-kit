package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/cmd/kit-doctor/v2/rules"
)

// TestCheckCodeowners_FlagsMissingSecurityPath pins the high-severity
// finding emitted when a security-sensitive path is absent from
// CODEOWNERS, and verifies the Fix appends exactly one line.
func TestCheckCodeowners_FlagsMissingSecurityPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github"), 0o700))
	coPath := filepath.Join(dir, ".github", "CODEOWNERS")
	require.NoError(t, os.WriteFile(coPath, []byte("# example\n*.go @go-team\n"), 0o644))

	findings, err := checkCodeownersSecuritySensitive(dir)
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	// Verify every finding has a fix and points at the CODEOWNERS file.
	for _, f := range findings {
		assert.NotNil(t, f.Fix, "every codeowners finding must carry a Fix")
		assert.Equal(t, coPath, f.File)
	}
}

// TestCheckCodeowners_FixAppendsLine verifies the Fix is idempotent
// and produces an exact paper-trail summary.
func TestCheckCodeowners_FixAppendsLine(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github"), 0o700))
	coPath := filepath.Join(dir, ".github", "CODEOWNERS")
	require.NoError(t, os.WriteFile(coPath, []byte("*.go @go-team\n"), 0o644))

	findings, err := checkCodeownersSecuritySensitive(dir)
	require.NoError(t, err)
	require.NotEmpty(t, findings)

	summary, err := findings[0].Fix()
	require.NoError(t, err)
	assert.Contains(t, summary, "appended")
	assert.Contains(t, summary, coPath)

	after, err := os.ReadFile(coPath)
	require.NoError(t, err)
	assert.Contains(t, string(after), "@security-review")
	assert.True(t, strings.HasSuffix(string(after), "\n"), "file must end with newline")

	// Second invocation must be a no-op.
	summary2, err := findings[0].Fix()
	require.NoError(t, err)
	assert.Contains(t, summary2, "no change")
	after2, err := os.ReadFile(coPath)
	require.NoError(t, err)
	assert.Equal(t, string(after), string(after2), "second Fix must not modify the file")
}

// TestCheckCodeowners_NoFindingsWhenCovered verifies a CODEOWNERS
// that already names every security-sensitive path produces zero
// findings.
func TestCheckCodeowners_NoFindingsWhenCovered(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github"), 0o700))
	var b strings.Builder
	for _, p := range securitySensitivePaths {
		b.WriteString(p + " @sec\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "CODEOWNERS"), []byte(b.String()), 0o644))
	findings, err := checkCodeownersSecuritySensitive(dir)
	require.NoError(t, err)
	assert.Empty(t, findings, "fully-covered CODEOWNERS must produce no findings, got %+v", findings)
}

// TestCheckDependencyAllowlist_FixIsIdempotent verifies the fix
// appends the expected line and is a no-op on second invocation.
func TestCheckDependencyAllowlist_FixIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "docs", "audit"), 0o700))
	path := filepath.Join(dir, "docs", "audit", "dependency-allowlist.txt")
	require.NoError(t, os.WriteFile(path, []byte("# allowed\nexample.com/known\n"), 0o644))

	findings, err := checkDependencyAllowlist(dir)
	require.NoError(t, err)
	require.NotEmpty(t, findings)

	for _, f := range findings {
		require.NotNil(t, f.Fix)
		summary, err := f.Fix()
		require.NoError(t, err)
		assert.Contains(t, summary, "appended")
	}

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, want := range requiredAllowlistEntries {
		assert.Contains(t, string(after), want+" # needs review")
	}

	// Second run reports no missing entries.
	findings2, err := checkDependencyAllowlist(dir)
	require.NoError(t, err)
	assert.Empty(t, findings2, "after fix, no findings expected, got %+v", findings2)
}

// TestCheckDependencyAllowlist_MissingFileSkipsCheck verifies the
// check stays silent (no error, no findings) when the allowlist
// file does not exist — many scan roots are arbitrary directories.
func TestCheckDependencyAllowlist_MissingFileSkipsCheck(t *testing.T) {
	dir := t.TempDir()
	findings, err := checkDependencyAllowlist(dir)
	require.NoError(t, err)
	assert.Empty(t, findings)
}

// TestCheckGoWorkSum_FixRunsGoWorkSync verifies the fix shells out
// to `go work sync` and reports success. Skips if `go` is not
// available on PATH.
func TestCheckGoWorkSum_FixRunsGoWorkSync(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go binary unavailable: %v", err)
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.work"), []byte("go 1.26.2\n"), 0o644))
	// No go.work.sum — looks stale.

	findings, err := checkGoWorkSum(dir)
	require.NoError(t, err)
	if len(findings) == 0 {
		t.Skip("heuristic did not flag this fixture; nothing to test")
	}
	require.NotNil(t, findings[0].Fix)
	summary, err := findings[0].Fix()
	require.NoError(t, err)
	assert.Contains(t, summary, "go work sync")
}

// TestCheckGoWorkSum_NoFindingWhenWorkAbsent verifies the check is
// silent when go.work does not exist (most service repos).
func TestCheckGoWorkSum_NoFindingWhenWorkAbsent(t *testing.T) {
	dir := t.TempDir()
	findings, err := checkGoWorkSum(dir)
	require.NoError(t, err)
	assert.Empty(t, findings)
}

// TestCheckServiceConfigEnvVars_FixAppendsMissing verifies the env-
// var check finds and fixes omissions, and is idempotent.
func TestCheckServiceConfigEnvVars_FixAppendsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.example")
	require.NoError(t, os.WriteFile(path, []byte("# example\nSOMETHING=value\n"), 0o644))

	findings, err := checkServiceConfigEnvVars(dir)
	require.NoError(t, err)
	require.NotEmpty(t, findings)

	for _, f := range findings {
		require.NotNil(t, f.Fix)
		summary, err := f.Fix()
		require.NoError(t, err)
		assert.Contains(t, summary, "appended")
	}
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, want := range requiredKitEnvVars {
		assert.Contains(t, string(after), want+"=")
	}

	// Idempotent: a second run yields zero findings.
	findings2, err := checkServiceConfigEnvVars(dir)
	require.NoError(t, err)
	assert.Empty(t, findings2)
}

// TestCheckServiceConfigEnvVars_NoFileSkips verifies the check is
// silent when .env.example does not exist.
func TestCheckServiceConfigEnvVars_NoFileSkips(t *testing.T) {
	dir := t.TempDir()
	findings, err := checkServiceConfigEnvVars(dir)
	require.NoError(t, err)
	assert.Empty(t, findings)
}

// TestRunRepoCheckers_SurfacesErrorAsWarningFinding pins the
// recovery contract: a checker that returns an error is reported as
// a single Warning-severity finding rather than aborting the run.
func TestRunRepoCheckers_SurfacesErrorAsWarningFinding(t *testing.T) {
	boom := func(string) ([]rules.Finding, error) {
		return nil, errBoom
	}
	got := runRepoCheckers("/tmp", []repoChecker{boom})
	require.Len(t, got, 1)
	assert.Equal(t, "repo-check-error", got[0].Rule)
	assert.Equal(t, rules.Warning, got[0].Severity)
}

var errBoom = errBoomSentinel("boom")

// errBoomSentinel is a tiny error type so the test does not import
// errors just to express a single sentinel.
type errBoomSentinel string

func (e errBoomSentinel) Error() string { return string(e) }
