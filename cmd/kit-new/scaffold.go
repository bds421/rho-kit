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
		return fmt.Errorf("kit-new: ServiceName must match lowercase kebab-case")
	}
	return nil
}

// modulePathElement matches a single slash-separated element of a Go
// module path. It is a conservative subset of what
// golang.org/x/mod/module.CheckPath accepts: ASCII letters, digits, and
// the punctuation Go allows in import paths (-._~). Crucially it admits
// no spaces, quotes, newlines, or other control characters, so the value
// cannot inject content into the rendered go.mod module line or the
// import strings in main.go/wire.go.
var modulePathElement = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._~-]*[A-Za-z0-9])?$`)

// ValidateModulePath rejects module paths that are not safe to render
// into go.mod's module directive or into Go import strings. It mirrors
// the rigor applied to ServiceName: a path with spaces, quotes, or
// newlines would otherwise produce a silently broken tree (discovered
// only at `go mod tidy`) or inject content into generated files. The
// invalid value is deliberately not echoed back in the error.
func ValidateModulePath(path string) error {
	if path == "" {
		return fmt.Errorf("kit-new: ModulePath must not be empty")
	}
	if strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return fmt.Errorf("kit-new: ModulePath must not start or end with a slash")
	}
	for _, elem := range strings.Split(path, "/") {
		if !modulePathElement.MatchString(elem) {
			return fmt.Errorf("kit-new: ModulePath must be a valid Go module path (slash-separated elements of letters, digits, and -._~)")
		}
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
	// wire.go.tmpl picks up the app/postgres adapter Module + migrations.
	// v2 made this the canonical data path; the kit no longer ships a
	// GORM scaffold. v2.0.0 moved Postgres wiring out of app/v2 into the
	// app/postgres sub-module so HTTP-only services don't pull in pgx.
	Postgres bool
	// Tenant scaffolds the multi-tenant Redis path: Redis config loading,
	// Builder.With(redis.Module(...)) + MultiTenant, and tenant-wrapped
	// Redis cache and idempotency stores so new services start from the
	// shared scoped-key encoder instead of hand-rolled prefixes. v2.0.0
	// moved Redis wiring out of app/v2 into app/redis.
	Tenant bool
	// Production enables the explicit resource-API baseline: Postgres,
	// RabbitMQ, JWT/JWKS verification, OpenFGA authorization, transactional
	// inbox/outbox, tracing, and committed contract artifacts. It deliberately
	// selects resource JWT auth; browser apps opt into app/oidc separately.
	Production bool
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
	{"production_consumer.go.tmpl", "internal/app/consumer.go", "Production"},
	{"production_schema.sql.tmpl", "db/migrations/00002_processed_commands.sql", "Production"},
	{"contract_manifest.json.tmpl", "contracts/contracts.json", "Production"},
	{"contract_openapi.json.tmpl", "contracts/openapi.json", "Production"},
	{"contract_event.json.tmpl", "contracts/events/command-processed.schema.json", "Production"},
	{"contract_event.json.tmpl", "internal/app/schemas/command-processed.schema.json", "Production"},
	{"production_env.tmpl", ".env.example", "Production"},
}

type plannedFile struct {
	tmplName string
	tmpl     *template.Template
	fullPath string
}

// scaffold writes the generated tree into outDir. It refuses to overwrite
// existing files and preflights every destination before creating anything.
func scaffold(outDir string, p Params) error {
	if p.Production {
		p.Postgres = true
	}
	if err := ValidateServiceName(p.ServiceName); err != nil {
		return err
	}
	if err := ValidateModulePath(p.ModulePath); err != nil {
		return err
	}
	if p.RhoVersion != "" && !rhoVersionPattern.MatchString(p.RhoVersion) {
		return fmt.Errorf("kit-new: RhoVersion must be a semver tag like v2.0.0 or a pseudo-version")
	}
	if p.GoVersion == "" {
		p.GoVersion = DefaultGoVersion
	} else if !goVersionPattern.MatchString(p.GoVersion) {
		return fmt.Errorf("kit-new: GoVersion must be major.minor like 1.26 (no patch, no prefix)")
	}

	absOutDir, err := filepath.Abs(outDir)
	if err != nil {
		return fmt.Errorf("kit-new: resolve output directory failed")
	}
	absOutDir = filepath.Clean(absOutDir)

	plan := make([]plannedFile, 0, len(templateFile))
	seen := make(map[string]string, len(templateFile))
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
			return fmt.Errorf("kit-new: parse destination path")
		}
		destPath, err := renderString(destT, p)
		if err != nil {
			return fmt.Errorf("kit-new: render destination path")
		}

		full := filepath.Clean(filepath.Join(absOutDir, destPath))
		if err := ensureContainedPath(absOutDir, full); err != nil {
			return fmt.Errorf("kit-new: rendered path escapes output directory")
		}
		if _, ok := seen[full]; ok {
			return fmt.Errorf("kit-new: templates render to the same destination")
		}
		seen[full] = row.tmpl
		if _, err := os.Lstat(full); err == nil {
			return fmt.Errorf("kit-new: destination already exists; refusing to overwrite")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("kit-new: inspect destination failed")
		}

		plan = append(plan, plannedFile{
			tmplName: row.tmpl,
			tmpl:     t,
			fullPath: full,
		})
	}

	if err := os.MkdirAll(absOutDir, 0o750); err != nil {
		return fmt.Errorf("kit-new: create output directory failed")
	}

	var created []string
	rollback := func() {
		for i := len(created) - 1; i >= 0; i-- {
			_ = os.Remove(created[i])
		}
		// Best-effort: remove the output dir if we created it empty of keepers.
		_ = os.Remove(absOutDir)
	}
	for _, file := range plan {
		parent := filepath.Dir(file.fullPath)
		if err := rejectSymlinkAncestors(absOutDir, file.fullPath); err != nil {
			rollback()
			return fmt.Errorf("kit-new: unsafe destination: %w", err)
		}
		if err := os.MkdirAll(parent, 0o750); err != nil {
			rollback()
			return fmt.Errorf("kit-new: create destination directory failed")
		}
		if err := rejectSymlinkAncestors(absOutDir, file.fullPath); err != nil {
			rollback()
			return fmt.Errorf("kit-new: unsafe destination: %w", err)
		}
		f, err := os.OpenFile(file.fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			rollback()
			if os.IsExist(err) {
				return fmt.Errorf("kit-new: destination already exists; refusing to overwrite")
			}
			return fmt.Errorf("kit-new: create destination failed")
		}
		created = append(created, file.fullPath)
		if err := file.tmpl.Execute(f, p); err != nil {
			_ = f.Close()
			rollback()
			return fmt.Errorf("kit-new: render template failed")
		}
		if err := f.Close(); err != nil {
			rollback()
			return fmt.Errorf("kit-new: close destination failed")
		}
	}
	return nil
}

func ensureContainedPath(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path escapes output directory")
	}
	return nil
}

func rejectSymlinkAncestors(root, target string) error {
	if err := ensureContainedPath(root, target); err != nil {
		return err
	}

	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("root directory is a symlink")
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("root path is not a directory")
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}

	cur := root
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component is a symlink")
		}
		if !info.IsDir() {
			return fmt.Errorf("path component is not a directory")
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
	// MCP/Tenant are handled inside shared templates; file-level gates keep
	// database and production-only artifacts out of the smaller profiles.
	switch gate {
	case "Postgres":
		return p.Postgres
	case "Production":
		return p.Production
	default:
		panic("kit-new: unknown template gate")
	}
}
