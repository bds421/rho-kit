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
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	actionlogpostgres "github.com/bds421/rho-kit/data/actionlog/postgres/v2"
	approvalpostgres "github.com/bds421/rho-kit/data/approval/postgres/v2"
	"github.com/bds421/rho-kit/data/idempotency/pgstore/v2"
	outboxpostgres "github.com/bds421/rho-kit/infra/outbox/postgres/v2"
	auditlogpostgres "github.com/bds421/rho-kit/observability/auditlog/postgres/v2"
)

// registry maps kit component names to their embedded migration
// filesystems. FR-006 [MED]: actionlog and approval pgx adapters
// also ship migrations; pre-fix only idempotency was registered, so
// services using those stores had no kit-tool way to discover and
// publish their schemas.
var registry = map[string]fs.FS{
	"idempotency": pgstore.Migrations,
	"actionlog":   actionlogpostgres.Migrations,
	"approval":    approvalpostgres.Migrations,
	"auditlog":    auditlogpostgres.Migrations,
	"outbox":      outboxpostgres.Migrations,
}

type registryEntry struct {
	name string
	fsys fs.FS
}

type migrationFile struct {
	component string
	filename  string
	target    string
	data      []byte
}

type publishPlan struct {
	publish []migrationFile
	drifted []migrationFile
	skipped int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stderr)
		return 2
	}

	switch args[0] {
	case "list":
		if len(args) > 1 {
			writef(stderr, "Error: list does not accept arguments\n")
			return 2
		}
		if err := cmdList(stdout); err != nil {
			writef(stderr, "Error listing migrations: %v\n", err)
			return 1
		}
		return 0
	case "publish":
		return cmdPublish(args[1:], stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	default:
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	writef(w, `Usage:
  kit-migrate list                    List available kit migrations
  kit-migrate publish --to=DIR        Copy kit migrations to DIR
  kit-migrate publish --to=DIR NAME   Copy only named component's migrations
  kit-migrate check --to=DIR          Detect drift between kit and on-disk migrations

Options:
  --to=DIR   Target migration directory (required for publish/check)
`)
}

func cmdList(stdout io.Writer) error {
	for _, entry := range registryEntries("") {
		files, err := listMigrations(entry.fsys)
		if err != nil {
			return fmt.Errorf("reading migrations failed")
		}
		writef(stdout, "%s:\n", entry.name)
		for _, f := range files {
			writef(stdout, "  %s\n", f)
		}
	}
	return nil
}

func cmdPublish(args []string, stdout, stderr io.Writer) int {
	targetDir, filterName, err := parseTargetAndComponent(args)
	if err != nil {
		writef(stderr, "Error: %v\n", err)
		return 2
	}

	if err := rejectSymlinkPathComponents(targetDir, targetDir); err != nil {
		writef(stderr, "Error preparing directory: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		writef(stderr, "Error creating directory: %v\n", err)
		return 1
	}
	if err := rejectSymlinkPathComponents(targetDir, targetDir); err != nil {
		writef(stderr, "Error preparing directory: %v\n", err)
		return 1
	}

	plan, err := buildPublishPlan(targetDir, filterName)
	if err != nil {
		writef(stderr, "Error preparing migrations: %v\n", err)
		return 1
	}

	for _, drifted := range plan.drifted {
		writef(
			stderr,
			"  drift: %s (from %s) - on-disk file differs from kit version; not overwritten\n",
			drifted.filename,
			drifted.component,
		)
	}
	if len(plan.drifted) > 0 {
		writef(
			stderr,
			"%d migration(s) have drifted from the kit version; refusing to publish new migrations\n",
			len(plan.drifted),
		)
		return 2
	}

	published := 0
	for _, migration := range plan.publish {
		if err := writeNewFile(targetDir, migration.target, migration.data, 0o644); err != nil {
			writef(stderr, "Error writing migration: %v\n", err)
			return 1
		}
		writef(stdout, "  published: %s (from %s)\n", migration.filename, migration.component)
		published++
	}

	switch {
	case published == 0 && plan.skipped > 0:
		writef(stdout, "All migrations already published (%d skipped)\n", plan.skipped)
	case published > 0:
		writef(stdout, "Published %d migration(s), %d already existed\n", published, plan.skipped)
	default:
		writeln(stdout, "No migrations to publish")
	}
	return 0
}

// cmdCheck reports any kit migration whose on-disk copy has diverged
// from the embedded version, without writing anything. Exits 0 when
// every kit migration is in sync (or absent), 2 when drift is detected.
// Designed for CI gates: `kit-migrate check --to=./migrations` is a
// pre-merge guard that fails the build when a teammate has hand-edited
// a kit-managed migration.
func cmdCheck(args []string, stdout, stderr io.Writer) int {
	targetDir, filterName, err := parseTargetAndComponent(args)
	if err != nil {
		writef(stderr, "Error: %v\n", err)
		return 2
	}
	if err := rejectSymlinkPathComponents(targetDir, targetDir); err != nil {
		writef(stderr, "Error preparing directory: %v\n", err)
		return 1
	}
	// A drift gate aimed at a directory that does not exist (e.g. a
	// typo'd path in CI) would otherwise read every migration as absent,
	// count zero, and print "OK: 0 migration(s) in sync" — a silent pass.
	// Treat a missing target directory as an error so the gate fails loud.
	if info, err := os.Stat(targetDir); err != nil {
		if os.IsNotExist(err) {
			writef(stderr, "Error: target directory does not exist\n")
			return 1
		}
		writef(stderr, "Error reading target directory: %v\n", err)
		return 1
	} else if !info.IsDir() {
		writef(stderr, "Error: target is not a directory\n")
		return 1
	}

	drifted := 0
	checked := 0

	for _, entry := range registryEntries(filterName) {
		files, err := listMigrations(entry.fsys)
		if err != nil {
			writef(stderr, "Error reading %s migrations: %v\n", entry.name, err)
			return 1
		}

		for _, filename := range files {
			targetPath, err := migrationTargetPath(targetDir, filename)
			if err != nil {
				writef(stderr, "Error resolving %s: %v\n", filename, err)
				return 1
			}
			if err := rejectSymlinkPathComponents(targetDir, filepath.Dir(targetPath)); err != nil {
				writef(stderr, "Error reading %s: %v\n", filename, err)
				return 1
			}
			if err := rejectSymlinkTarget(targetPath); err != nil {
				writef(stderr, "Error reading %s: %v\n", filename, err)
				return 1
			}
			existing, err := os.ReadFile(targetPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				writef(stderr, "Error reading %s\n", filename)
				return 1
			}

			data, err := fs.ReadFile(entry.fsys, "migrations/"+filename)
			if err != nil {
				writef(stderr, "Error reading embedded %s: %v\n", filename, err)
				return 1
			}

			checked++
			if !bytes.Equal(existing, data) {
				writef(stdout, "  drift: %s (from %s)\n", filename, entry.name)
				drifted++
			}
		}
	}

	if drifted > 0 {
		writef(stderr, "%d migration(s) have drifted from the kit version\n", drifted)
		return 2
	}
	writef(stdout, "OK: %d migration(s) in sync with kit\n", checked)
	return 0
}

func writef(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func writeln(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}

func parseTargetAndComponent(args []string) (string, string, error) {
	var targetDir string
	var component string

	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--to="):
			value := strings.TrimPrefix(arg, "--to=")
			if value == "" {
				return "", "", fmt.Errorf("--to requires a non-empty value")
			}
			if targetDir != "" {
				return "", "", fmt.Errorf("--to may only be specified once")
			}
			targetDir = value
		case arg == "--to":
			return "", "", fmt.Errorf("--to requires a value (use --to=DIR)")
		case strings.HasPrefix(arg, "-"):
			return "", "", fmt.Errorf("unknown flag")
		default:
			if arg == "" {
				return "", "", fmt.Errorf("component name cannot be empty")
			}
			if component != "" {
				return "", "", fmt.Errorf("only one component may be specified")
			}
			component = arg
		}
	}

	if targetDir == "" {
		return "", "", fmt.Errorf("--to=DIR is required")
	}
	if component != "" {
		if _, ok := registry[component]; !ok {
			return "", "", fmt.Errorf("unknown component")
		}
	}
	return targetDir, component, nil
}

func buildPublishPlan(targetDir, filterName string) (publishPlan, error) {
	var plan publishPlan

	if err := checkDuplicateVersions(filterName); err != nil {
		return publishPlan{}, err
	}

	for _, entry := range registryEntries(filterName) {
		files, err := listMigrations(entry.fsys)
		if err != nil {
			return publishPlan{}, fmt.Errorf("reading migrations failed")
		}

		for _, filename := range files {
			data, err := fs.ReadFile(entry.fsys, "migrations/"+filename)
			if err != nil {
				return publishPlan{}, fmt.Errorf("reading embedded migration failed")
			}

			migration := migrationFile{
				component: entry.name,
				filename:  filename,
				data:      data,
			}
			migration.target, err = migrationTargetPath(targetDir, filename)
			if err != nil {
				return publishPlan{}, fmt.Errorf("resolving migration target failed")
			}
			if err := rejectSymlinkPathComponents(targetDir, filepath.Dir(migration.target)); err != nil {
				return publishPlan{}, fmt.Errorf("reading migration target failed: %w", err)
			}
			if err := rejectSymlinkTarget(migration.target); err != nil {
				return publishPlan{}, fmt.Errorf("reading migration target failed: %w", err)
			}

			existing, err := os.ReadFile(migration.target)
			if err == nil {
				if bytes.Equal(existing, data) {
					plan.skipped++
				} else {
					plan.drifted = append(plan.drifted, migration)
				}
				continue
			}
			if !os.IsNotExist(err) {
				return publishPlan{}, fmt.Errorf("reading migration target failed")
			}
			plan.publish = append(plan.publish, migration)
		}
	}

	return plan, nil
}

// gooseVersion returns the numeric goose version prefix of a migration
// filename (the leading run of characters before the first '_'), e.g.
// "20260514000001" for "20260514000001_create_outbox_entries.sql".
// goose collects migrations by this version, so two files sharing it in
// one directory are a "duplicate migration version" that goose refuses
// to run.
func gooseVersion(filename string) string {
	if i := strings.IndexByte(filename, '_'); i >= 0 {
		return filename[:i]
	}
	return filename
}

// checkDuplicateVersions fails the publish when two distinct kit
// migrations selected by filterName share the same goose version
// prefix. A publish flattens every selected component's migrations into
// one directory; if two of them collide on version, goose errors with
// "found duplicate migration version" on the next `goose up`, leaving
// the service unable to migrate. Detecting the collision before writing
// anything turns a silent footgun into an actionable error.
func checkDuplicateVersions(filterName string) error {
	seen := make(map[string]string)
	for _, entry := range registryEntries(filterName) {
		files, err := listMigrations(entry.fsys)
		if err != nil {
			return fmt.Errorf("reading migrations failed")
		}
		for _, filename := range files {
			version := gooseVersion(filename)
			prev, ok := seen[version]
			if ok && prev != filename {
				return fmt.Errorf(
					"duplicate goose version %s shared by %s and %s; goose refuses to run a directory with duplicate versions (publish a single component with the NAME argument instead)",
					version, prev, filename,
				)
			}
			seen[version] = filename
		}
	}
	return nil
}

func migrationTargetPath(targetDir, filename string) (string, error) {
	target := filepath.Clean(filepath.Join(targetDir, filename))
	absDir, err := filepath.Abs(targetDir)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(filepath.Clean(absDir), filepath.Clean(absTarget))
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("target path escapes target directory")
	}
	return target, nil
}

func rejectSymlinkTarget(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("target is a symlink; refusing to follow it")
	}
	return nil
}

func rejectSymlinkPathComponents(root, path string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absRoot = filepath.Clean(absRoot)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	absPath = filepath.Clean(absPath)
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path escapes target directory")
	}
	if err := rejectSymlinkTarget(absRoot); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}

	cur := absRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component is a symlink; refusing to follow it")
		}
		if !info.IsDir() && filepath.Clean(cur) != absPath {
			return fmt.Errorf("path component is not a directory")
		}
	}
	return nil
}

func writeNewFile(root, path string, data []byte, perm fs.FileMode) error {
	if err := rejectSymlinkPathComponents(root, filepath.Dir(path)); err != nil {
		return err
	}
	if err := rejectSymlinkTarget(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func registryEntries(filterName string) []registryEntry {
	names := componentNames()
	entries := make([]registryEntry, 0, len(names))
	for _, name := range names {
		if filterName != "" && name != filterName {
			continue
		}
		entries = append(entries, registryEntry{name: name, fsys: registry[name]})
	}
	return entries
}

func componentNames() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	sort.Strings(names)
	return names, nil
}
