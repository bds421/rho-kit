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
}

// templateFile maps a template name to its destination path within
// the generated tree. Add a row to ship a new file from a new
// template.
var templateFile = []struct {
	tmpl string // file under templates/
	dest string // path relative to output dir; supports {{.ServiceName}}
}{
	{"main.go.tmpl", "cmd/{{.ServiceName}}/main.go"},
	{"wire.go.tmpl", "internal/app/wire.go"},
	{"go.mod.tmpl", "go.mod"},
	{"README.md.tmpl", "README.md"},
	{"Makefile.tmpl", "Makefile"},
	{"AGENTS.md.tmpl", "AGENTS.md"},
	{"ci.yml.tmpl", ".github/workflows/ci.yml"},
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
