package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, src string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

// History passes, a new contracting migration and a stale history entry
// fail, and folders outside the kit's sets are not read.
func TestRun(t *testing.T) {
	root := t.TempDir()
	drop := "-- +goose Up\nDROP TABLE t;\n"
	write(t, filepath.Join(root, "data/a/migrations/00001_a.sql"), drop)
	write(t, filepath.Join(root, "data/a/testdata/00001_x.sql"), drop)
	write(t, filepath.Join(root, "data/a/queries/q.sql"), "SELECT 1;\n")
	var out bytes.Buffer
	if code := run(root, map[string]int64{"data/a/migrations": 1}, &out, &out); code != 0 {
		t.Fatalf("history: %d %s", code, out.String())
	}
	write(t, filepath.Join(root, "data/a/migrations/00002_b.sql"), drop)
	out.Reset()
	if code := run(root, map[string]int64{"data/a/migrations": 1, "gone/migrations": 3}, &out, &out); code != 1 ||
		!strings.Contains(out.String(), "data/a/migrations/00002_b.sql:2:") || !strings.Contains(out.String(), "gone/migrations has no migrations") {
		t.Fatalf("%d %s", code, out.String())
	}
}
