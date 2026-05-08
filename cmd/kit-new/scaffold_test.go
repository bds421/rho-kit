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

func TestScaffold_GeneratedWireUsesAppBuilder(t *testing.T) {
	out := t.TempDir()
	require.NoError(t, scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
	}))

	wireBody, err := os.ReadFile(filepath.Join(out, "internal/app/wire.go"))
	require.NoError(t, err)
	wire := string(wireBody)
	// app.Builder is the canonical entry point — it composes
	// httpx.NewServer under the hood plus the production-safety
	// validator, signed-request / tenant / budget middleware, and the
	// auto-applied default stack. Forking the wiring directly to
	// httpx.NewServer here re-introduces the gaps Builder closes.
	assert.NotContains(t, wire, "&http.Server{",
		"generated wire.go must not construct net/http.Server directly — use app.Builder")
	assert.NotContains(t, wire, "httpx.NewServer(",
		"generated wire.go must not call httpx.NewServer directly — use app.Builder, which calls httpx.NewServer with the kit's middleware chain wired in")
	assert.Contains(t, wire, `"github.com/bds421/rho-kit/app/v2"`,
		"generated wire.go must import the kit's app package")
	assert.Contains(t, wire, "kitapp.New(",
		"generated wire.go must build the service via app.Builder")
	assert.Contains(t, wire, "Run()",
		"generated wire.go must call Builder.Run() so signal handling and lifecycle ordering come from the kit")
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
	assert.Contains(t, string(gomod), "github.com/bds421/rho-kit/httpx/v2 v2.0.0",
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

	// Modules transitively required at v0.0.0 by httpx, app, and
	// every consolidated v2 module they pull in. Until the kit
	// publishes real v2 tags, the in-repo build test resolves them
	// via local replaces — the same pattern the workspace uses. v2
	// collapsed the per-package modules into a small set of parents,
	// listed below.
	internal := []string{
		"app",
		"authz",
		"core",
		"crypto",
		"data",
		"flags",
		"grpcx",
		"httpx",
		"infra",
		"io",
		"observability",
		"resilience",
		"runtime",
		"security",
		// Adapter modules pulled transitively by app's WithRabbitMQ /
		// WithNATS / WithRedis / WithPgx wirings. They're optional at
		// runtime but the import graph reaches them at build time.
		"infra/messaging/amqpbackend",
		"infra/messaging/natsbackend",
		"infra/redis",
		"infra/sqldb/pgx",
	}
	replaceArgs := []string{"mod", "edit"}
	for _, sub := range internal {
		dir := filepath.Join(repoRoot, sub)
		if _, statErr := os.Stat(dir); statErr != nil {
			t.Skipf("local checkout not found at %s: %v", dir, statErr)
		}
		replaceArgs = append(replaceArgs,
			"-replace=github.com/bds421/rho-kit/"+sub+"/v2="+dir,
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
	assert.Contains(t, wire, `"github.com/bds421/rho-kit/httpx/v2/mcp"`,
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

	// With an explicit RhoVersion the generated go.mod pins httpx
	// (which now contains the mcp sub-package after the v2
	// consolidation) at that version.
	gomod, err := os.ReadFile(filepath.Join(out, "go.mod"))
	require.NoError(t, err)
	assert.Contains(t, string(gomod), "github.com/bds421/rho-kit/httpx/v2 v2.0.0")
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

	// The plain skeleton's wire.go must not import the mcp
	// sub-package. After the v2 module consolidation, mcp lives
	// inside httpx, so we check the import path rather than the
	// go.mod (which only ever pins parent modules).
	wireSrc, err := os.ReadFile(filepath.Join(out, "internal/app/wire.go"))
	require.NoError(t, err)
	assert.NotContains(t, string(wireSrc), "httpx/mcp",
		"plain wire.go must not import the mcp package")
}
