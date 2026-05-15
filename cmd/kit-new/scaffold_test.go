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

func TestGateActive_PanicsOnUnknownGateWithoutReflectingName(t *testing.T) {
	assert.PanicsWithValue(t, "kit-new: unknown template gate", func() {
		gateActive(Params{}, "secret-token")
	})
}

func TestSplitLeadingServiceName(t *testing.T) {
	name, args := splitLeadingServiceName([]string{"demo", "-module-path", "example.com/demo", "-tenant"})
	assert.Equal(t, "demo", name)
	assert.Equal(t, []string{"-module-path", "example.com/demo", "-tenant"}, args)

	name, args = splitLeadingServiceName([]string{"-module-path", "example.com/demo", "demo"})
	assert.Empty(t, name)
	assert.Equal(t, []string{"-module-path", "example.com/demo", "demo"}, args)
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
	assert.NotContains(t, err.Error(), "latest")
}

func TestScaffold_RejectsEmptyServiceName(t *testing.T) {
	require.Error(t, scaffold(t.TempDir(), Params{ModulePath: "example.com/demo"}))
}

func TestScaffold_RejectsTraversalServiceName(t *testing.T) {
	out := t.TempDir()
	err := scaffold(out, Params{ServiceName: "../../outside", ModulePath: "example.com/demo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ServiceName")
	assert.NotContains(t, err.Error(), "../../outside")

	siblings, _ := os.ReadDir(filepath.Dir(out))
	for _, e := range siblings {
		assert.NotEqual(t, "outside", e.Name(), "scaffold must not write outside outDir")
	}
}

func TestScaffold_RejectsUppercaseServiceName(t *testing.T) {
	err := scaffold(t.TempDir(), Params{ServiceName: "MyService", ModulePath: "example.com/demo"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "MyService")
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

func TestScaffold_RefusesToOverwriteExistingFiles(t *testing.T) {
	out := t.TempDir()
	readmePath := filepath.Join(out, "README.md")
	original := []byte("hand-written service notes\n")
	require.NoError(t, os.WriteFile(readmePath, original, 0o644))

	err := scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to overwrite")
	assert.NotContains(t, err.Error(), readmePath)

	got, readErr := os.ReadFile(readmePath)
	require.NoError(t, readErr)
	assert.Equal(t, original, got)
	_, statErr := os.Stat(filepath.Join(out, "cmd/demo/main.go"))
	assert.True(t, os.IsNotExist(statErr), "scaffold must preflight before writing any new files")
}

func TestScaffold_RejectsSymlinkParent(t *testing.T) {
	out := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(out, "cmd")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
	assert.NotContains(t, err.Error(), link)
	assert.NotContains(t, err.Error(), outside)

	_, statErr := os.Stat(filepath.Join(outside, "demo", "main.go"))
	assert.True(t, os.IsNotExist(statErr), "scaffold must not write through symlinked parent")
}

func TestScaffold_RejectsSymlinkOutputRoot(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	out := filepath.Join(parent, "service")
	if err := os.Symlink(outside, out); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
	assert.NotContains(t, err.Error(), out)
	assert.NotContains(t, err.Error(), outside)

	_, statErr := os.Stat(filepath.Join(outside, "cmd", "demo", "main.go"))
	assert.True(t, os.IsNotExist(statErr), "scaffold must not write through symlinked root")
}

func TestScaffold_GeneratedTreeBuildsAndPasses(t *testing.T) {
	runScaffoldBuildTest(t, Params{})
}

func TestScaffold_MCPGeneratedTreeBuildsAndPasses(t *testing.T) {
	runScaffoldBuildTest(t, Params{MCP: true})
}

// TestScaffold_PostgresGeneratedTreeBuildsAndPasses regression-tests
// FR-002 + FR-003: the -postgres scaffold variant must compile after
// `go mod tidy`. Pre-fix this never ran from the CLI (no -postgres
// flag), and the embed.FS in wire.go.tmpl pointed at the wrong
// directory, so the generated service failed to build with "no
// matching files found" before any code path could be exercised.
func TestScaffold_PostgresGeneratedTreeBuildsAndPasses(t *testing.T) {
	runScaffoldBuildTest(t, Params{Postgres: true})
}

func TestScaffold_PostgresAndMCPGeneratedTreeBuildsAndPasses(t *testing.T) {
	runScaffoldBuildTest(t, Params{Postgres: true, MCP: true})
}

func TestScaffold_TenantGeneratedTreeBuildsAndPasses(t *testing.T) {
	runScaffoldBuildTest(t, Params{Tenant: true})
}

func TestScaffold_AllFeaturesGeneratedTreeBuildsAndPasses(t *testing.T) {
	runScaffoldBuildTest(t, Params{Postgres: true, MCP: true, Tenant: true})
}

// runScaffoldBuildTest scaffolds a fresh tree, points the kit
// require at the local checkout (the only practical way to run a
// downstream-style build before the kit publishes a tag), then runs
// `go mod tidy && go build && go vet`. Covers every scaffold variant
// so default / MCP / Postgres / Postgres+MCP all have the same
// compile guarantee.
func runScaffoldBuildTest(t *testing.T, opts Params) {
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
		MCP:         opts.MCP,
		Postgres:    opts.Postgres,
		Tenant:      opts.Tenant,
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
		"app/amqp",
		"app/auditlog",
		"app/cron",
		"app/flags",
		"app/grpc",
		"app/http",
		"app/jwt",
		"app/leader",
		"app/nats",
		"app/paseto",
		"app/postgres",
		"app/ratelimit",
		"app/redis",
		"app/signedrequest",
		"app/slo",
		"app/storage",
		"app/tracing",
		"authz",
		"core",
		"crypto",
		"data",
		"data/cache/rediscache",
		"data/idempotency/redisstore",
		"flags",
		"grpcx",
		"httpx",
		"infra",
		"io",
		"observability",
		"resilience",
		"runtime",
		"security",
		// v2.0.0 lazy-adapter sub-modules: app/postgres, app/redis, app/amqp,
		// app/nats, app/tracing, app/grpc each own their heavy dep (pgx,
		// go-redis, amqp091, nats.go, otel, grpc-go) and re-export thin
		// Module() constructors. They appear above so the resolver can
		// satisfy build-time imports against the local checkout.
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

	doctorCmd := exec.Command("go", "run", "./cmd/kit-doctor", out)
	doctorCmd.Dir = repoRoot
	doctorCmd.Env = append(os.Environ(), "GOWORK="+filepath.Join(repoRoot, "go.work"))
	if buf, err := doctorCmd.CombinedOutput(); err != nil {
		t.Fatalf("generated service failed kit-doctor:\n%s", buf)
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

func TestScaffold_TenantFlag_WiresTenantWrappers(t *testing.T) {
	out := t.TempDir()
	require.NoError(t, scaffold(out, Params{
		ServiceName: "demo",
		ModulePath:  "example.com/demo",
		Tenant:      true,
		RhoVersion:  "v2.0.0",
	}))

	wireBody, err := os.ReadFile(filepath.Join(out, "internal/app/wire.go"))
	require.NoError(t, err)
	wire := string(wireBody)
	assert.Contains(t, wire, "kitredis.LoadFields()",
		"tenant scaffold must load Redis settings through the kit")
	assert.Contains(t, wire, "MultiTenant(httpxtenant.HeaderExtractor(\"X-Tenant-Id\"))",
		"tenant scaffold must enable strict tenant extraction")
	assert.Contains(t, wire, "tenantcache.Wrap(baseCache)",
		"tenant scaffold must wrap Redis cache with the tenant-scoped cache")
	assert.Contains(t, wire, "tenantidempotency.Wrap(redisstore.New(",
		"tenant scaffold must wrap Redis idempotency with the tenant-scoped store")
	assert.Contains(t, wire, "httpidempotency.Middleware(tenantDeps.Idempotency",
		"tenant scaffold must route idempotency through the wrapped store")
	assert.NotContains(t, wire, `mux.HandleFunc("/healthz"`,
		"tenant scaffold must keep unauthenticated probes on the internal ops listener, not the tenant-scoped public mux")
	assert.NotContains(t, wire, "tenant:",
		"tenant scaffold must not hand-roll tenant key prefixes")

	gomod, err := os.ReadFile(filepath.Join(out, "go.mod"))
	require.NoError(t, err)
	mod := string(gomod)
	assert.Contains(t, mod, "github.com/bds421/rho-kit/data/cache/rediscache/v2 v2.0.0")
	assert.Contains(t, mod, "github.com/bds421/rho-kit/data/idempotency/redisstore/v2 v2.0.0")
	assert.Contains(t, mod, "github.com/bds421/rho-kit/data/v2 v2.0.0")
	assert.Contains(t, mod, "github.com/bds421/rho-kit/infra/redis/v2 v2.0.0")
}
