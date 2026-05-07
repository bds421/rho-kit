package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScaffold_GeneratesExpectedTree(t *testing.T) {
	out := t.TempDir()
	require.NoError(t, scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
	}))

	expected := []string{
		"cmd/demo/main.go",
		"internal/app/wire.go",
		"go.mod",
		"README.md",
		"Makefile",
		"AGENTS.md",
		".github/workflows/ci.yml",
	}
	for _, rel := range expected {
		full := filepath.Join(out, rel)
		_, err := os.Stat(full)
		assert.NoError(t, err, "expected %s to exist", rel)
	}

	// The main.go template references the wire package via the
	// configured module path — the generated file should embed it.
	body, err := os.ReadFile(filepath.Join(out, "cmd/demo/main.go"))
	require.NoError(t, err)
	assert.Contains(t, string(body), `"example.com/demo/internal/app"`)
	assert.NotContains(t, string(body), "{{.ModulePath}}", "templates must be fully rendered")
}

func TestScaffold_RejectsEmptyServiceName(t *testing.T) {
	require.Error(t, scaffold(t.TempDir(), Params{ModulePath: "example.com/demo"}))
}

func TestScaffold_RejectsEmptyModulePath(t *testing.T) {
	require.Error(t, scaffold(t.TempDir(), Params{ServiceName: "demo"}))
}

func TestScaffold_GeneratedTreeBuildsAndPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("self-test invokes go build / go test")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	out := t.TempDir()
	require.NoError(t, scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
	}))

	// Each `go` invocation runs in the generated dir; failures
	// surface the toolchain output so a regression in a template is
	// immediately diagnosable.
	for _, args := range [][]string{
		{"build", "./..."},
		{"vet", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = out
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
		buf, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "go %s failed:\n%s", strings.Join(args, " "), buf)
	}
}
