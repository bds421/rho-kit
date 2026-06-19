package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsErrorIdent(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		// Canonical local error variables — must be flagged.
		{"err", true},
		{"perr", true},
		{"rerr", true},
		{"gErr", true},
		{"slErr", true},
		{"saveErr", true},
		{"closeErr", true},
		{"relErr", true},
		{"ctxErr", true},
		// Locals the old closed whitelist missed (wave-136 regression):
		// any lower-cased local backend error must be flagged.
		{"marshalErr", true},
		{"unmarshalErr", true},
		{"loadErr", true},
		{"storeErr", true},
		{"readErr", true},
		{"writeErr", true},
		{"dialErr", true},
		{"e", true},
		// Package-level sentinels (exported, Err-prefixed) are safe to
		// render verbatim and must NOT be flagged as a bare ident.
		{"ErrValidation", false},
		{"ErrBatchTooLarge", false},
		{"ErrObjectNotFound", false},
		{"Err", false},
		// Non-error placeholders must never be flagged.
		{"nil", false},
		{"_", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isErrorIdent(tt.name); got != tt.want {
				t.Errorf("isErrorIdent(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestScanFile(t *testing.T) {
	const src = `package fixture

import "fmt"

var ErrSentinel = fmt.Errorf("sentinel")

func wrapMarshal(marshalErr error) error {
	// Local backend error — must be flagged.
	return fmt.Errorf("cache compute marshal: %w", marshalErr)
}

func wrapErr(err error) error {
	// Canonical err — must be flagged.
	return fmt.Errorf("op: %w", err)
}

func wrapSentinel() error {
	// Exported package-level sentinel — must NOT be flagged.
	return fmt.Errorf("op: %w", ErrSentinel)
}

func wrapQualified(err error) error {
	// Package-qualified sentinel (selector) — must NOT be flagged.
	return fmt.Errorf("op: %w", fmt.ErrSomething)
}

func wrapOptedOut(loadErr error) error {
	// Opt-out marker keeps a deliberate wrap visible at review.
	return fmt.Errorf("op: %w", loadErr) // kit:ok-fmt-errorf-wrap
}

func notAWrap(err error) error {
	// No ": %w" segment — not a violation.
	return fmt.Errorf("op: %v", err)
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := scanFile(path)
	if err != nil {
		t.Fatalf("scanFile: %v", err)
	}

	gotIdents := map[string]bool{}
	for _, v := range got {
		gotIdents[v.expr] = true
	}

	wantFlagged := []string{
		`fmt.Errorf("cache compute marshal: %w", ..., marshalErr)`,
		`fmt.Errorf("op: %w", ..., err)`,
	}
	for _, w := range wantFlagged {
		if !gotIdents[w] {
			t.Errorf("expected violation %q, got %v", w, gotIdents)
		}
	}

	if len(got) != len(wantFlagged) {
		t.Errorf("expected exactly %d violations, got %d: %v", len(wantFlagged), len(got), gotIdents)
	}
}

// TestScanFileAliasedImport asserts that an aliased fmt import does not let
// a %w-over-local wrap slip past the gate, and that a same-named selector
// from an *unrelated* package (when fmt is not the aliased target) is not
// falsely flagged.
func TestScanFileAliasedImport(t *testing.T) {
	const src = `package fixture

import f "fmt"

func wrapAliased(err error) error {
	// Aliased fmt still wraps a local backend error — must be flagged.
	return f.Errorf("op: %w", err)
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "aliased.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, err := scanFile(path)
	if err != nil {
		t.Fatalf("scanFile: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 violation through aliased fmt, got %d: %v", len(got), got)
	}
	if got[0].expr != `fmt.Errorf("op: %w", ..., err)` {
		t.Errorf("unexpected violation expr: %q", got[0].expr)
	}
}

// TestScanFileDotImport asserts that a dot-imported fmt (Errorf as a bare
// ident) is still detected.
func TestScanFileDotImport(t *testing.T) {
	const src = `package fixture

import . "fmt"

func wrapDot(err error) error {
	// Dot-imported fmt: Errorf is a bare ident — must be flagged.
	return Errorf("op: %w", err)
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "dot.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, err := scanFile(path)
	if err != nil {
		t.Fatalf("scanFile: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 violation through dot-imported fmt, got %d: %v", len(got), got)
	}
}

// TestScanFileUnrelatedAlias guards against a false positive: a selector
// f.Errorf where f is a *different* package (fmt imported normally, an alias
// f bound to something else) must not be flagged as fmt.Errorf.
func TestScanFileUnrelatedAlias(t *testing.T) {
	const src = `package fixture

import (
	"fmt"
	f "errors"
)

var _ = fmt.Sprintf

func notFmt(err error) error {
	// f is errors, not fmt; f.Errorf is not the stdlib fmt.Errorf.
	return f.Errorf("op: %w", err)
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "unrelated.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, err := scanFile(path)
	if err != nil {
		t.Fatalf("scanFile: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no violations (f is errors, not fmt), got %d: %v", len(got), got)
	}
}
