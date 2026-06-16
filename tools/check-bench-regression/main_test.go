package main

import (
	"reflect"
	"testing"
)

func TestParseBenchOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []benchResult
	}{
		{
			name: "standard line with all metrics",
			out:  "BenchmarkWrapError-8   	10000000	        13.2 ns/op	      32 B/op	       1 allocs/op\n",
			want: []benchResult{{Name: "BenchmarkWrapError", NsPerOp: 13.2, Allocs: 1, Bytes: 32}},
		},
		{
			name: "best (lowest ns) wins across runs",
			out: "BenchmarkWrapError-8   	1	        20 ns/op	      32 B/op	       1 allocs/op\n" +
				"BenchmarkWrapError-8   	1	        13 ns/op	      32 B/op	       1 allocs/op\n",
			want: []benchResult{{Name: "BenchmarkWrapError", NsPerOp: 13, Allocs: 1, Bytes: 32}},
		},
		{
			// A genuine sub-1ns benchmark prints "0.0000 ns/op" but is a
			// real, completed measurement and must NOT be silently dropped:
			// otherwise it never reaches the baseline comparison and never
			// trips the "NOT in baseline" notice.
			name: "zero ns benchmark is retained",
			out:  "BenchmarkWrapError_NilError-8   	1000000000	         0.0000 ns/op	       0 B/op	       0 allocs/op\n",
			want: []benchResult{{Name: "BenchmarkWrapError_NilError", NsPerOp: 0, Allocs: 0, Bytes: 0}},
		},
		{
			// A line that names a benchmark but carries no completed
			// ns/op measurement (e.g. an in-progress / informational
			// line) must be skipped, not treated as a 0-ns result.
			name: "benchmark line without ns/op field is skipped",
			out:  "BenchmarkWrapError-8   	1000000000\n",
			want: []benchResult{},
		},
		{
			name: "non-benchmark lines ignored",
			out:  "PASS\nok  \tgithub.com/x/y\t1.234s\n",
			want: []benchResult{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBenchOutput(tt.out)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseBenchOutput()\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}
