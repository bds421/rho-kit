// Command kit-migrate publishes kit-provided database migrations into a
// service's migration directory. Run it once during project setup, then
// goose manages the migrations from there.
//
// Usage:
//
//	go run github.com/bds421/rho-kit/cmd/kit-migrate publish --to=./migrations
//	go run github.com/bds421/rho-kit/cmd/kit-migrate list
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bds421/rho-kit/data/idempotency/pgstore"
	"github.com/bds421/rho-kit/observability/auditlog/gormstore"
)

// registry maps kit component names to their embedded migration filesystems.
// Add new entries here when kit packages provide migrations.
var registry = map[string]fs.FS{
	"auditlog":    gormstore.Migrations,
	"idempotency": pgstore.Migrations,
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "list":
		cmdList()
	case "publish":
		cmdPublish()
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  kit-migrate list                    List available kit migrations
  kit-migrate publish --to=DIR        Copy kit migrations to DIR
  kit-migrate publish --to=DIR NAME   Copy only named component's migrations

Options:
  --to=DIR   Target migration directory (required for publish)
`)
}

func cmdList() {
	for name, fsys := range registry {
		files, _ := listMigrations(fsys)
		fmt.Printf("%s:\n", name)
		for _, f := range files {
			fmt.Printf("  %s\n", f)
		}
	}
}

func cmdPublish() {
	var targetDir string
	var filterName string

	for _, arg := range os.Args[2:] {
		if len(arg) > 5 && arg[:5] == "--to=" {
			targetDir = arg[5:]
		} else if arg[0] != '-' {
			filterName = arg
		}
	}

	if targetDir == "" {
		fmt.Fprintf(os.Stderr, "Error: --to=DIR is required\n")
		os.Exit(1)
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
		os.Exit(1)
	}

	published := 0
	skipped := 0

	for name, fsys := range registry {
		if filterName != "" && name != filterName {
			continue
		}

		files, err := listMigrations(fsys)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading %s migrations: %v\n", name, err)
			os.Exit(1)
		}

		for _, filename := range files {
			targetPath := filepath.Join(targetDir, filename)

			// Idempotent: skip if already exists
			if _, err := os.Stat(targetPath); err == nil {
				skipped++
				continue
			}

			data, err := fs.ReadFile(fsys, "migrations/"+filename)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", filename, err)
				os.Exit(1)
			}

			if err := os.WriteFile(targetPath, data, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", targetPath, err)
				os.Exit(1)
			}

			fmt.Printf("  published: %s (from %s)\n", filename, name)
			published++
		}
	}

	if published == 0 && skipped > 0 {
		fmt.Printf("All migrations already published (%d skipped)\n", skipped)
	} else if published > 0 {
		fmt.Printf("Published %d migration(s), %d already existed\n", published, skipped)
	} else {
		fmt.Println("No migrations to publish")
	}
}

// listMigrations returns the filenames from the migrations/ subdirectory.
func listMigrations(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
