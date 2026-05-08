// Command kit-migrate publishes kit-provided database migrations into a
// service's migration directory. Run it once during project setup, then
// goose manages the migrations from there.
//
// Usage:
//
//	go run github.com/bds421/rho-kit/cmd/kit-migrate/v2 publish --to=./migrations
//	go run github.com/bds421/rho-kit/cmd/kit-migrate/v2 list
//	go run github.com/bds421/rho-kit/cmd/kit-migrate/v2 check --to=./migrations
package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bds421/rho-kit/data/idempotency/pgstore/v2"
)

// registry maps kit component names to their embedded migration
// filesystems. Add new entries here when kit packages provide
// migrations. v2 dropped auditlog/gormstore — a pgx-native auditlog
// store with its own migrations is a follow-up; the actionlog and
// approval pgx adapters already ship their own.
var registry = map[string]fs.FS{
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
	case "check":
		cmdCheck()
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
  kit-migrate check --to=DIR          Detect drift between kit and on-disk migrations

Options:
  --to=DIR   Target migration directory (required for publish/check)
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
		switch {
		case len(arg) > 5 && arg[:5] == "--to=":
			targetDir = arg[5:]
		case arg == "--to":
			fmt.Fprintf(os.Stderr, "Error: --to requires a value (use --to=DIR)\n")
			os.Exit(1)
		case len(arg) > 0 && arg[0] != '-':
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
	drifted := 0

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

			data, err := fs.ReadFile(fsys, "migrations/"+filename)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", filename, err)
				os.Exit(1)
			}

			// Detect drift: a kit-published migration that has been
			// edited locally is a silent forward-compat hazard. The
			// kit ships a fix, the operator has diverged, the next
			// publish does nothing — and the bug stays. Compare
			// bytes; on mismatch, warn but do not overwrite.
			if existing, statErr := os.ReadFile(targetPath); statErr == nil {
				if !bytes.Equal(existing, data) {
					fmt.Fprintf(os.Stderr, "  drift: %s (from %s) — on-disk file differs from kit version; not overwritten\n", filename, name)
					drifted++
				} else {
					skipped++
				}
				continue
			}

			if err := os.WriteFile(targetPath, data, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", targetPath, err)
				os.Exit(1)
			}

			fmt.Printf("  published: %s (from %s)\n", filename, name)
			published++
		}
	}

	switch {
	case published == 0 && skipped > 0 && drifted == 0:
		fmt.Printf("All migrations already published (%d skipped)\n", skipped)
	case published > 0:
		fmt.Printf("Published %d migration(s), %d already existed, %d drifted\n", published, skipped, drifted)
	case drifted > 0:
		fmt.Printf("No new migrations published; %d drifted (see warnings above)\n", drifted)
		os.Exit(2)
	default:
		fmt.Println("No migrations to publish")
	}
}

// cmdCheck reports any kit migration whose on-disk copy has diverged
// from the embedded version, without writing anything. Exits 0 when
// every kit migration is in sync (or absent), 2 when drift is detected.
// Designed for CI gates: `kit-migrate check --to=./migrations` is a
// pre-merge guard that fails the build when a teammate has hand-edited
// a kit-managed migration.
func cmdCheck() {
	var targetDir string
	var filterName string

	for _, arg := range os.Args[2:] {
		switch {
		case len(arg) > 5 && arg[:5] == "--to=":
			targetDir = arg[5:]
		case arg == "--to":
			fmt.Fprintf(os.Stderr, "Error: --to requires a value (use --to=DIR)\n")
			os.Exit(1)
		case len(arg) > 0 && arg[0] != '-':
			filterName = arg
		}
	}

	if targetDir == "" {
		fmt.Fprintf(os.Stderr, "Error: --to=DIR is required\n")
		os.Exit(1)
	}

	drifted := 0
	checked := 0

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
			existing, err := os.ReadFile(targetPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", targetPath, err)
				os.Exit(1)
			}

			data, err := fs.ReadFile(fsys, "migrations/"+filename)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading embedded %s: %v\n", filename, err)
				os.Exit(1)
			}

			checked++
			if !bytes.Equal(existing, data) {
				fmt.Printf("  drift: %s (from %s)\n", filename, name)
				drifted++
			}
		}
	}

	if drifted > 0 {
		fmt.Fprintf(os.Stderr, "%d migration(s) have drifted from the kit version\n", drifted)
		os.Exit(2)
	}
	fmt.Printf("OK: %d migration(s) in sync with kit\n", checked)
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
