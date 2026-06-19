package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// helper: collect the shipped wave numbers as a sorted slice for easy
// comparison in table-driven tests.
func shippedSlice(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

func TestParseShippedWaves(t *testing.T) {
	tests := []struct {
		name   string
		gitLog string
		want   []int
	}{
		{
			name: "documented conventional-commit shape counts as shipped",
			gitLog: "feat(v2): add WithOpaqueConsumeLabels in wave 140\n" +
				"fix(v2): patch wave 7\n" +
				"refactor(v2): rework wave 12\n",
			want: []int{7, 12, 140},
		},
		{
			name: "non-v2 scope must NOT count as shipped",
			gitLog: "fix(kit-doctor): foo wave 5\n" +
				"feat(netutil): bar wave 6\n",
			want: []int{},
		},
		{
			name: "disallowed commit type must NOT count as shipped",
			gitLog: "style(v2): tidy wave 9\n" +
				"revert: wip wave 10\n" +
				"wip: wave 11\n",
			want: []int{},
		},
		{
			name: "casual mention without conventional prefix must NOT count",
			gitLog: "Merge branch covering wave 4\n" +
				"docs(audit): record Wave 4+5\n",
			// Only docs(v2) would qualify; docs(audit) does not, and a
			// bare merge subject does not.
			want: []int{},
		},
		{
			name: "combined 'wave 4+5' under (v2) scope records both numbers",
			gitLog: "docs(v2): record Wave 4+5\n" +
				"feat(v2): wire wave 6\n",
			want: []int{4, 5, 6},
		},
		{
			name: "all documented types are accepted with (v2) scope",
			gitLog: "feat(v2): wave 1\n" +
				"fix(v2): wave 2\n" +
				"refactor(v2): wave 3\n" +
				"chore(v2): wave 4\n" +
				"docs(v2): wave 5\n" +
				"test(v2): wave 6\n" +
				"perf(v2): wave 7\n",
			want: []int{1, 2, 3, 4, 5, 6, 7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shippedSlice(parseShippedWaves(tt.gitLog))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseShippedWaves(%q) = %v, want %v", tt.gitLog, got, tt.want)
			}
		})
	}
}

// TestFutureWaveRE pins the unanchored future-wave matcher, including the
// end-of-line "wave" case that the original `wave[^0-9]` form missed (a
// phrase such as "tracked for the next wave" with no trailing character).
func TestFutureWaveRE(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"tracked for ... wave at end of line", "A toggle is tracked for the next wave", true},
		{"tracked for ... wave with trailing punctuation", "A toggle is tracked for the next wave.", true},
		{"tracked for wave at end of line (no number)", "This is tracked for wave", true},
		{"future wave phrasing", "deferred to a future wave for now", true},
		{"follow-up wave phrasing", "deferred to a follow-up wave", true},
		{"post-2.0.0 phrasing", "left for post-2.0.0", true},
		{"concrete wave number is not an unanchored future-wave match", "shipped in wave 140", false},
		{"plain prose without a future-wave promise", "the release is complete", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := futureWaveRE.MatchString(tt.line); got != tt.want {
				t.Errorf("futureWaveRE.MatchString(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// TestScanFileUnanchoredFutureWaveAtLineEnd is the end-to-end contract for
// the regex fix: an unanchored "tracked for ... wave" claim that ends the
// line (no specific wave number) must be flagged as doc rot.
func TestScanFileUnanchoredFutureWaveAtLineEnd(t *testing.T) {
	const doc = "# Notes\n" +
		"A consumer toggle is tracked for the next wave\n" +
		"Shipped feature lives in wave 140\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	// wave 140 is shipped so the concrete reference does not flag; only the
	// unanchored end-of-line "wave" claim should surface.
	got, err := scanFile(path, map[int]bool{140: true}, false)
	if err != nil {
		t.Fatalf("scanFile: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 unanchored future-wave finding, got %d: %+v", len(got), got)
	}
	if got[0].line != 2 {
		t.Errorf("expected finding on line 2, got line %d", got[0].line)
	}
}

// TestScanFileOptOutSuppressesUnanchored confirms the line-level opt-out
// marker still suppresses an end-of-line future-wave claim after the fix.
func TestScanFileOptOutSuppressesUnanchored(t *testing.T) {
	const doc = "A consumer toggle is tracked for the next wave <!-- kit:ok-doc-rot -->\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	got, err := scanFile(path, map[int]bool{}, false)
	if err != nil {
		t.Fatalf("scanFile: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected opt-out marker to suppress the claim, got %d: %+v", len(got), got)
	}
}
