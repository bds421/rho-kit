package main

import (
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
