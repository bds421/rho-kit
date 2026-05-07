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

func TestScaffold_RejectsTraversalServiceName(t *testing.T) {
	out := t.TempDir()
	err := scaffold(out, Params{ServiceName: "../../outside", ModulePath: "example.com/demo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ServiceName")

	siblings, _ := os.ReadDir(filepath.Dir(out))
	for _, e := range siblings {
		assert.NotEqual(t, "outside", e.Name(), "scaffold must not write outside outDir")
	}
}

func TestScaffold_RejectsUppercaseServiceName(t *testing.T) {
	require.Error(t, scaffold(t.TempDir(), Params{ServiceName: "MyService", ModulePath: "example.com/demo"}))
}

func TestScaffold_AcceptsKebabServiceName(t *testing.T) {
	out := t.TempDir()
	require.NoError(t, scaffold(out, Params{ServiceName: "my-service", ModulePath: "example.com/my-service"}))
	_, err := os.Stat(filepath.Join(out, "cmd/my-service/main.go"))
	require.NoError(t, err)
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

func TestScaffold_MCPFlag_ScaffoldsToolRegistration(t *testing.T) {
	out := t.TempDir()
	require.NoError(t, scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
		MCP:         true,
	}))

	wireBody, err := os.ReadFile(filepath.Join(out, "internal/app/wire.go"))
	require.NoError(t, err)
	wire := string(wireBody)
	assert.Contains(t, wire, `"github.com/bds421/rho-kit/httpx/mcp"`,
		"MCP scaffold must import the mcp package")
	assert.Contains(t, wire, "mcp.Register[EchoIn, EchoOut]",
		"MCP scaffold must call mcp.Register")
	assert.Contains(t, wire, `mux.Handle("/mcp",`,
		"MCP scaffold must mount the JSON-RPC handler")
	assert.NotContains(t, wire, "{{", "wire.go must be fully rendered")

	makefile, err := os.ReadFile(filepath.Join(out, "Makefile"))
	require.NoError(t, err)
	mk := string(makefile)
	assert.Contains(t, mk, "mcp-smoke", "Makefile must include the smoke-test target")
	assert.Contains(t, mk, "tools/list",
		"Makefile smoke-test must call the JSON-RPC tools/list method")

	// go.mod must reference the kit's mcp package so the generated
	// service can be `go mod tidy`'d cleanly.
	gomod, err := os.ReadFile(filepath.Join(out, "go.mod"))
	require.NoError(t, err)
	assert.Contains(t, string(gomod), "github.com/bds421/rho-kit/httpx/mcp")
}

func TestScaffold_NoMCP_ProducesPlainSkeleton(t *testing.T) {
	out := t.TempDir()
	require.NoError(t, scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
	}))

	wireBody, err := os.ReadFile(filepath.Join(out, "internal/app/wire.go"))
	require.NoError(t, err)
	assert.NotContains(t, string(wireBody), "mcp.NewServer",
		"plain scaffold must not pull in MCP scaffolding")

	makefile, err := os.ReadFile(filepath.Join(out, "Makefile"))
	require.NoError(t, err)
	assert.NotContains(t, string(makefile), "mcp-smoke",
		"plain Makefile must not include the smoke-test target")

	gomod, err := os.ReadFile(filepath.Join(out, "go.mod"))
	require.NoError(t, err)
	assert.NotContains(t, string(gomod), "httpx/mcp",
		"plain go.mod must not declare an mcp dependency")
}
