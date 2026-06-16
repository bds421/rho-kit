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
