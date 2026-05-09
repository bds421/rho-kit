package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

var serviceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ValidateServiceName rejects names that are not safe path segments
// or that violate the lowercase-kebab convention used by the
// scaffolded module path.
func ValidateServiceName(name string) error {
	if name == "" {
		return fmt.Errorf("kit-new: ServiceName must not be empty")
	}
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("kit-new: ServiceName %q must match %s", name, serviceNamePattern)
	}
	return nil
}

//go:embed templates/*.tmpl
var templatesFS embed.FS

// Params drives every template's data context.
type Params struct {
	ServiceName string
	ModulePath  string
	// MCP toggles the scaffolded sample MCP tool registration. When
	// true, wire.go.tmpl includes a `mcp.NewServer` block, and
	// Makefile.tmpl gains a `mcp-smoke` target that POSTs a
	// `tools/list` JSON-RPC call against a locally-running service.
	MCP bool
	// RhoVersion pins the rho-kit module versions written into the
	// generated go.mod. Empty means no explicit require — the
	// generated tree relies on `go mod tidy` populating require
	// blocks from imports against whatever the consumer's environment
	// resolves (proxy, GOPROXY, replaces). Set to a concrete tag
	// (e.g. "v2.0.0") to lock the scaffold to a known release.
	RhoVersion string
	// GoVersion is the minimum-required go version emitted into
	// go.mod's `go` directive. Empty (the default) renders [DefaultGoVersion],
	// a bare major.minor line so downstream toolchains pick up patch
	// updates automatically. Override only when targeting a specific
	// major.minor floor.
	GoVersion string
	// Postgres scaffolds the sqlc + pgx golden path: sqlc.yaml,
	// db/queries/*.sql, db/sqlc/ output dir, Makefile generate target,
	// wire.go.tmpl picks up app.Builder.WithPostgres + WithMigrations.
	// v2 made this the canonical data path; the kit no longer ships a
	// GORM scaffold.
	Postgres bool
}

// DefaultGoVersion is the bare major.minor line emitted into the
// scaffolded go.mod when [Params.GoVersion] is empty. Tracks the
// kit's minimum supported toolchain — bumped in a coordinated PR
// when the kit raises its floor.
const DefaultGoVersion = "1.26"

// rhoVersionPattern accepts the version specifiers Go accepts in a
// go.mod require directive: full semver tags ("v2.0.0",
// "v2.0.0-rc1") and pseudo-versions
// ("v0.0.0-20240101120000-abcdef012345"). Accepting "latest" or other
// non-version sentinels would produce a go.mod that does not parse.
var rhoVersionPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// goVersionPattern accepts only major.minor (e.g. "1.26"). Patch
// versions are deliberately rejected: a `go` directive in go.mod is a
// minimum-required floor, and pinning to a patch version forces every
// downstream toolchain to that exact build for no benefit.
var goVersionPattern = regexp.MustCompile(`^\d+\.\d+$`)

// templateFile maps a template name to its destination path within
// the generated tree. Add a row to ship a new file from a new
// template.
var templateFile = []struct {
	tmpl string // file under templates/
	dest string // path relative to output dir; supports {{.ServiceName}}
	gate string // optional Params field name; row is skipped when the field is false
}{
	{"main.go.tmpl", "cmd/{{.ServiceName}}/main.go", ""},
	{"wire.go.tmpl", "internal/app/wire.go", ""},
	{"go.mod.tmpl", "go.mod", ""},
	{"README.md.tmpl", "README.md", ""},
	{"Makefile.tmpl", "Makefile", ""},
	{"AGENTS.md.tmpl", "AGENTS.md", ""},
	{"ci.yml.tmpl", ".github/workflows/ci.yml", ""},

	// Postgres + sqlc golden path. Gated on Params.Postgres so a
	// consumer scaffolding a no-DB service (e.g., a plain HTTP
	// proxy) doesn't get an unused sqlc.yaml. The migrations package
	// owns the embed.FS so go:embed resolves relative to the .sql
	// files, not relative to internal/app/wire.go (which would search
	// for the wrong directory — audit FR-003).
	{"sqlc.yaml.tmpl", "sqlc.yaml", "Postgres"},
	{"db_query_sample.sql.tmpl", "db/queries/users.sql", "Postgres"},
	{"db_schema_sample.sql.tmpl", "db/migrations/00001_users.sql", "Postgres"},
	{"db_migrations_pkg.go.tmpl", "db/migrations/migrations.go", "Postgres"},
}

// scaffold writes the generated tree into outDir. Returns an error on
// the first template or filesystem failure (no partial-state cleanup
// — callers writing to a fresh directory get a clear half-written
// tree they can `rm -rf`).
func scaffold(outDir string, p Params) error {
	if err := ValidateServiceName(p.ServiceName); err != nil {
		return err
	}
	if p.ModulePath == "" {
		return fmt.Errorf("kit-new: ModulePath must not be empty")
	}
	if p.RhoVersion != "" && !rhoVersionPattern.MatchString(p.RhoVersion) {
		return fmt.Errorf("kit-new: RhoVersion %q must be a semver tag like v2.0.0 or a pseudo-version", p.RhoVersion)
	}
	if p.GoVersion == "" {
		p.GoVersion = DefaultGoVersion
	} else if !goVersionPattern.MatchString(p.GoVersion) {
		return fmt.Errorf("kit-new: GoVersion %q must be major.minor like 1.26 (no patch, no prefix)", p.GoVersion)
	}

	absOutDir, err := filepath.Abs(outDir)
	if err != nil {
		return fmt.Errorf("kit-new: resolve outDir %q: %w", outDir, err)
	}
	absOutDir = filepath.Clean(absOutDir)
	if err := os.MkdirAll(absOutDir, 0o750); err != nil {
		return fmt.Errorf("kit-new: mkdir %q: %w", absOutDir, err)
	}
	prefix := absOutDir + string(filepath.Separator)

	for _, row := range templateFile {
		if row.gate != "" && !gateActive(p, row.gate) {
			continue
		}
		body, err := fs.ReadFile(templatesFS, "templates/"+row.tmpl)
		if err != nil {
			return fmt.Errorf("kit-new: read template %q: %w", row.tmpl, err)
		}
		t, err := template.New(row.tmpl).Parse(string(body))
		if err != nil {
			return fmt.Errorf("kit-new: parse template %q: %w", row.tmpl, err)
		}

		// Render destination path through the same template engine so
		// {{.ServiceName}} placeholders work in dest paths too.
		destT, err := template.New("dest:" + row.tmpl).Parse(row.dest)
		if err != nil {
			return fmt.Errorf("kit-new: parse dest path %q: %w", row.dest, err)
		}
		destPath, err := renderString(destT, p)
		if err != nil {
			return fmt.Errorf("kit-new: render dest path %q: %w", row.dest, err)
		}

		full := filepath.Clean(filepath.Join(absOutDir, destPath))
		if full != absOutDir && !strings.HasPrefix(full, prefix) {
			return fmt.Errorf("kit-new: rendered path %q escapes outDir %q", full, absOutDir)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return fmt.Errorf("kit-new: mkdir %q: %w", filepath.Dir(full), err)
		}
		f, err := os.Create(full)
		if err != nil {
			return fmt.Errorf("kit-new: create %q: %w", full, err)
		}
		if err := t.Execute(f, p); err != nil {
			_ = f.Close()
			return fmt.Errorf("kit-new: render %q: %w", row.tmpl, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("kit-new: close %q: %w", full, err)
		}
	}
	return nil
}

func renderString(t *template.Template, data any) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// gateActive reports whether a templateFile row's gate (a Params bool
// field name like "Postgres" or "MCP") is true. Unknown gate names
// panic — they indicate a code-side typo, not a runtime user error.
func gateActive(p Params, gate string) bool {
	switch gate {
	case "Postgres":
		return p.Postgres
	case "MCP":
		return p.MCP
	default:
		panic("kit-new: unknown template gate " + gate)
	}
}
