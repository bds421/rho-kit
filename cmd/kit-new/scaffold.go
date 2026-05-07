package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// Params drives every template's data context.
type Params struct {
	ServiceName string
	ModulePath  string
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
	if p.ServiceName == "" {
		return fmt.Errorf("kit-new: ServiceName must not be empty")
	}
	if p.ModulePath == "" {
		return fmt.Errorf("kit-new: ModulePath must not be empty")
	}

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

		full := filepath.Join(outDir, destPath)
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
