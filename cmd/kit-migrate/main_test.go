package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestParseTargetAndComponent(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantDir string
		wantCmp string
		wantErr string
	}{
		{
			name:    "all components",
			args:    []string{"--to=/tmp/migrations"},
			wantDir: "/tmp/migrations",
		},
		{
			name:    "known component",
			args:    []string{"--to=/tmp/migrations", "idempotency"},
			wantDir: "/tmp/migrations",
			wantCmp: "idempotency",
		},
		{
			name:    "unknown component",
			args:    []string{"--to=/tmp/migrations", "missing"},
			wantErr: "unknown component",
		},
		{
			name:    "unknown flag",
			args:    []string{"--to=/tmp/migrations", "--force"},
			wantErr: "unknown flag",
		},
		{
			name:    "multiple components",
			args:    []string{"--to=/tmp/migrations", "idempotency", "approval"},
			wantErr: "only one component may be specified",
		},
		{
			name:    "missing to",
			args:    []string{"idempotency"},
			wantErr: "--to=DIR is required",
		},
		{
			name:    "to without value",
			args:    []string{"--to"},
			wantErr: "--to requires a value",
		},
		{
			name:    "empty to value",
			args:    []string{"--to="},
			wantErr: "--to requires a non-empty value",
		},
		{
			name:    "duplicate to",
			args:    []string{"--to=/tmp/a", "--to=/tmp/b"},
			wantErr: "--to may only be specified once",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDir, gotCmp, err := parseTargetAndComponent(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				if strings.Contains(err.Error(), "missing") || strings.Contains(err.Error(), "--force") {
					t.Fatalf("error reflected rejected argument: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotDir != tt.wantDir {
				t.Fatalf("targetDir = %q, want %q", gotDir, tt.wantDir)
			}
			if gotCmp != tt.wantCmp {
				t.Fatalf("component = %q, want %q", gotCmp, tt.wantCmp)
			}
		})
	}
}

func TestComponentNamesSorted(t *testing.T) {
	names := componentNames()
	if !sort.StringsAreSorted(names) {
		t.Fatalf("componentNames() = %v, want sorted", names)
	}

	for _, want := range []string{"actionlog", "approval", "auditlog", "idempotency", "outbox"} {
		if !contains(names, want) {
			t.Fatalf("componentNames() = %v, missing %q", names, want)
		}
	}
}

func TestRunListUsesSortedRegistry(t *testing.T) {
	code, stdout, stderr := runCommand("list")
	if code != 0 {
		t.Fatalf("run list code = %d, stderr = %q", code, stderr)
	}
	// Sorted alphabetically: actionlog → approval → auditlog → idempotency → outbox.
	assertInOrder(t, stdout, "actionlog:\n", "approval:\n", "auditlog:\n", "idempotency:\n", "outbox:\n")
}

func TestRunListRejectsArguments(t *testing.T) {
	code, _, stderr := runCommand("list", "idempotency")
	if code != 2 {
		t.Fatalf("run list with arg code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "list does not accept arguments") {
		t.Fatalf("stderr = %q, want list argument error", stderr)
	}
}

func TestPublishRefusesPartialWriteWhenDriftExists(t *testing.T) {
	dir := t.TempDir()

	code, _, stderr := runCommand("publish", "--to="+dir, "idempotency")
	if code != 0 {
		t.Fatalf("initial publish code = %d, stderr = %q", code, stderr)
	}

	files, err := listMigrations(registry["idempotency"])
	if err != nil {
		t.Fatalf("list idempotency migrations: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("test needs at least two idempotency migrations, got %d", len(files))
	}

	driftedPath := filepath.Join(dir, files[0])
	missingPath := filepath.Join(dir, files[1])
	driftedData := []byte("local edit\n")
	if err := os.WriteFile(driftedPath, driftedData, 0o644); err != nil {
		t.Fatalf("write drifted migration: %v", err)
	}
	if err := os.Remove(missingPath); err != nil {
		t.Fatalf("remove migration to prove no partial publish: %v", err)
	}

	code, _, stderr = runCommand("publish", "--to="+dir, "idempotency")
	if code != 2 {
		t.Fatalf("publish with drift code = %d, want 2; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "refusing to publish new migrations") {
		t.Fatalf("stderr = %q, want refusal message", stderr)
	}
	if _, err := os.Stat(missingPath); !os.IsNotExist(err) {
		t.Fatalf("missing migration was written despite drift; stat error = %v", err)
	}
	gotDriftedData, err := os.ReadFile(driftedPath)
	if err != nil {
		t.Fatalf("read drifted migration: %v", err)
	}
	if !bytes.Equal(gotDriftedData, driftedData) {
		t.Fatalf("drifted migration was overwritten")
	}
}

func TestPublishRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()

	files, err := listMigrations(registry["idempotency"])
	if err != nil {
		t.Fatalf("list idempotency migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("test needs at least one idempotency migration")
	}

	outside := filepath.Join(outsideDir, "outside.sql")
	target := filepath.Join(dir, files[0])
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	code, _, stderr := runCommand("publish", "--to="+dir, "idempotency")
	if code != 1 {
		t.Fatalf("publish with symlink code = %d, want 1; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "symlink") {
		t.Fatalf("stderr = %q, want symlink refusal", stderr)
	}
	if strings.Contains(stderr, target) || strings.Contains(stderr, outside) {
		t.Fatalf("stderr reflected filesystem path: %q", stderr)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside target was created through symlink; stat error = %v", err)
	}
}

func TestPublishRejectsSymlinkTargetDir(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()
	targetDir := filepath.Join(dir, "migrations")
	if err := os.Symlink(outsideDir, targetDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	code, _, stderr := runCommand("publish", "--to="+targetDir, "idempotency")
	if code != 1 {
		t.Fatalf("publish with symlink target dir code = %d, want 1; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "symlink") {
		t.Fatalf("stderr = %q, want symlink refusal", stderr)
	}
	if strings.Contains(stderr, targetDir) || strings.Contains(stderr, outsideDir) {
		t.Fatalf("stderr reflected filesystem path: %q", stderr)
	}

	entries, err := os.ReadDir(outsideDir)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("outside dir received published files: %v", entries)
	}
}

// TestPublishRejectsDuplicateGooseVersion guards FR-601: when a
// publish flattens migrations from multiple kit components into one
// directory, two components may ship the same numeric goose version
// prefix (e.g. auditlog 20260514000001_create_audit_log_events and
// outbox 20260514000001_create_outbox_entries). goose refuses to
// "up" a directory with a duplicate version ("found duplicate
// migration version"), so kit-migrate must detect the collision and
// refuse to publish a directory that goose would reject — rather than
// silently produce one.
func TestPublishRejectsDuplicateGooseVersion(t *testing.T) {
	dir := t.TempDir()

	code, stdout, stderr := runCommand("publish", "--to="+dir)
	if code == 0 {
		t.Fatalf("publish-all succeeded but the kit registry ships duplicate goose versions; goose would reject the result. stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stderr, "duplicate") {
		t.Fatalf("stderr = %q, want a duplicate-version refusal", stderr)
	}
}

// TestCheckRejectsMissingTargetDir guards FR-601 (check side): a CI
// drift gate aimed at a typo'd directory must not silently pass. When
// --to points at a directory that does not exist, check must exit
// non-zero rather than print "OK: 0 migration(s) in sync".
func TestCheckRejectsMissingTargetDir(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")

	code, stdout, stderr := runCommand("check", "--to="+missing)
	if code == 0 {
		t.Fatalf("check against a missing directory exited 0; a drift gate must fail loudly. stdout=%q stderr=%q", stdout, stderr)
	}
	if strings.Contains(stdout, "OK:") {
		t.Fatalf("check printed an OK summary for a missing directory: stdout=%q", stdout)
	}
	if strings.Contains(stderr, missing) {
		t.Fatalf("stderr reflected filesystem path: %q", stderr)
	}
}

func runCommand(args ...string) (int, string, string) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func assertInOrder(t *testing.T, haystack string, needles ...string) {
	t.Helper()

	nextStart := 0
	for _, needle := range needles {
		idx := strings.Index(haystack[nextStart:], needle)
		if idx < 0 {
			t.Fatalf("%q not found after byte %d in:\n%s", needle, nextStart, haystack)
		}
		nextStart += idx + len(needle)
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// TestPublish_AuditlogAndOutboxRoundTrip guards L200: kit-migrate
// must produce on-disk migration files byte-for-byte identical to
// the embedded migrations.FS for the auditlog and outbox kit
// components (added in waves 60/61). Without this test, a future
// change to the embed.FS shape (e.g. a renamed file or a stray
// editor backup checked in) could silently change what services
// receive when they run `kit-migrate publish --to=./migrations`.
//
// The matching embedded migrations are exercised end-to-end against
// a real Postgres in
// observability/auditlog/postgres/integrationtest and
// infra/outbox/postgres/integrationtest. This test proves the kit-
// migrate publish path produces the SAME files the integration
// tests apply, so the transitive guarantee is "kit-migrate output
// works against real Postgres" without re-spinning a container.
func TestPublish_AuditlogAndOutboxRoundTrip(t *testing.T) {
	for _, component := range []string{"auditlog", "outbox"} {
		t.Run(component, func(t *testing.T) {
			dir := t.TempDir()
			code, _, stderr := runCommand("publish", "--to="+dir, component)
			if code != 0 {
				t.Fatalf("publish %s code=%d, stderr=%q", component, code, stderr)
			}

			// Compare every published file byte-for-byte against the
			// embedded migrations FS.
			fsys := registry[component]
			err := fs.WalkDir(fsys, "migrations", func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if d.IsDir() {
					return nil
				}
				want, readErr := fs.ReadFile(fsys, path)
				if readErr != nil {
					return readErr
				}
				// Embedded path is "migrations/<file>"; published path
				// is "<dir>/<file>" (no migrations/ prefix — kit-migrate
				// flattens).
				rel := filepath.Base(path)
				got, readErr := os.ReadFile(filepath.Join(dir, rel))
				if readErr != nil {
					t.Fatalf("read published %s: %v", rel, readErr)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("published %s differs from embedded migrations/%s; kit-migrate publish must be a byte-for-byte copy", rel, rel)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk embedded FS: %v", err)
			}

			// Also verify the published directory contains at least one
			// migration file (catches an empty-publish regression).
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("read published dir: %v", err)
			}
			if len(entries) == 0 {
				t.Fatalf("publish %s produced an empty target directory", component)
			}
		})
	}
}
