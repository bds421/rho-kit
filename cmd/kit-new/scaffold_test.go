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

func TestScaffold_GeneratedWireUsesKitHTTPServer(t *testing.T) {
	out := t.TempDir()
	require.NoError(t, scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
	}))

	wireBody, err := os.ReadFile(filepath.Join(out, "internal/app/wire.go"))
	require.NoError(t, err)
	wire := string(wireBody)
	assert.NotContains(t, wire, "&http.Server{",
		"generated wire.go must not construct net/http.Server directly — use httpx.NewServer for slowloris timeouts and structured error log")
	assert.Contains(t, wire, "httpx.NewServer(",
		"generated wire.go must call httpx.NewServer so the service inherits the kit's HTTP defaults")
	assert.Contains(t, wire, `"github.com/bds421/rho-kit/httpx"`,
		"generated wire.go must import the kit's httpx package")
}

func TestScaffold_DefaultGoModHasNoUnpublishablePin(t *testing.T) {
	// Without -rho-version, the generated go.mod must not pin
	// internal modules to v0.0.0 (the prior placeholder did this and
	// broke downstream resolution against the public proxy). Empty
	// require block is fine — `go mod tidy` populates it from
	// imports.
	out := t.TempDir()
	require.NoError(t, scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
	}))

	gomod, err := os.ReadFile(filepath.Join(out, "go.mod"))
	require.NoError(t, err)
	body := string(gomod)
	assert.NotContains(t, body, "v0.0.0",
		"generated go.mod must not pin any module to v0.0.0; that breaks downstream consumers")
	assert.Contains(t, body, "module example.com/demo")
}

func TestScaffold_RhoVersionFlagWritesRequire(t *testing.T) {
	out := t.TempDir()
	require.NoError(t, scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
		RhoVersion:  "v2.0.0",
	}))

	gomod, err := os.ReadFile(filepath.Join(out, "go.mod"))
	require.NoError(t, err)
	assert.Contains(t, string(gomod), "github.com/bds421/rho-kit/httpx v2.0.0",
		"generated go.mod must pin to the requested version when RhoVersion is set")
}

func TestScaffold_RhoVersionRejectsInvalidString(t *testing.T) {
	err := scaffold(t.TempDir(), Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
		RhoVersion:  "latest",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RhoVersion")
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
	runScaffoldBuildTest(t, false)
}

func TestScaffold_MCPGeneratedTreeBuildsAndPasses(t *testing.T) {
	runScaffoldBuildTest(t, true)
}

// runScaffoldBuildTest scaffolds a fresh tree, points the kit
// require at the local checkout (the only practical way to run a
// downstream-style build before the kit publishes a tag), then runs
// `go mod tidy && go build && go vet`. Covers both the default
// scaffold and the MCP variant so the MCP path has the same compile
// guarantee as the default.
func runScaffoldBuildTest(t *testing.T, mcp bool) {
	t.Helper()
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
		MCP:         mcp,
	}))

	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	httpxDir := filepath.Join(repoRoot, "httpx")
	if _, statErr := os.Stat(httpxDir); statErr != nil {
		t.Skipf("httpx local checkout not found at %s: %v", httpxDir, statErr)
	}

	// Modules transitively required at v0.0.0 by httpx and
	// httpx/mcp. Until the kit publishes real tags, the in-repo
	// build test resolves them via local replaces — the same pattern
	// the workspace uses. The release pipeline strips these
	// replaces before tagging (see hierarchical-release.sh).
	internal := []string{"httpx"}
	if mcp {
		internal = append(internal,
			"httpx/mcp",
			"core/tenant",
			"core/validate",
			"data/actionlog",
			"data/actionlog/memory",
		)
	}
	replaceArgs := []string{"mod", "edit"}
	for _, sub := range internal {
		dir := filepath.Join(repoRoot, sub)
		if _, statErr := os.Stat(dir); statErr != nil {
			t.Skipf("local checkout not found at %s: %v", dir, statErr)
		}
		replaceArgs = append(replaceArgs,
			"-replace=github.com/bds421/rho-kit/"+sub+"="+dir,
		)
	}
	replaceCmd := exec.Command("go", replaceArgs...)
	replaceCmd.Dir = out
	replaceCmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	if buf, err := replaceCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod edit -replace failed:\n%s", buf)
	}
	tidyCmd := exec.Command("go", "mod", "tidy")
	tidyCmd.Dir = out
	tidyCmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	if buf, err := tidyCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy failed:\n%s", buf)
	}

	for _, args := range [][]string{
		{"build", "./..."},
		{"vet", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = out
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
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
		RhoVersion:  "v2.0.0",
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

	// With an explicit RhoVersion the generated go.mod pins both
	// httpx and httpx/mcp to that version.
	gomod, err := os.ReadFile(filepath.Join(out, "go.mod"))
	require.NoError(t, err)
	assert.Contains(t, string(gomod), "github.com/bds421/rho-kit/httpx/mcp v2.0.0")
}

func TestScaffold_NoMCP_ProducesPlainSkeleton(t *testing.T) {
	out := t.TempDir()
	require.NoError(t, scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
		RhoVersion:  "v2.0.0",
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
