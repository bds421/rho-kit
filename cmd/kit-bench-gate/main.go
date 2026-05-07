// Command kit-bench-gate compares two `go test -bench` text outputs
// and exits non-zero when any tracked metric regresses past the
// configured threshold.
//
// Usage:
//
//	go test -bench=. -benchmem -count=5 ./... > current.txt
//	kit-bench-gate -baseline bench/baselines/current.txt \
//	               -current current.txt \
//	               -threshold 10 \
//	               -fail-on ns/op,allocs/op
//
// Exit codes:
//   - 0: no regression at or above -threshold for any tracked metric.
//   - 1: at least one tracked metric regressed past the threshold.
//   - 2: tool error (file not found, parse failure).
//
// The -fail-on flag is a comma-separated list of metric names —
// a regression in `B/op` will appear in the output but only fail CI
// if `B/op` is in -fail-on. This lets teams ratchet up gradually
// (start with `ns/op` only, add `allocs/op` once the codebase is
// clean).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	baseline := flag.String("baseline", "", "path to baseline `go test -bench` output")
	current := flag.String("current", "", "path to current `go test -bench` output")
	threshold := flag.Float64("threshold", 10, "percent regression that fails the gate")
	failOnFlag := flag.String("fail-on", "ns/op", "comma-separated metrics that fail the gate (ns/op,B/op,allocs/op)")
	flag.Parse()

	if *baseline == "" || *current == "" {
		fmt.Fprintln(os.Stderr, "usage: kit-bench-gate -baseline FILE -current FILE [-threshold 10] [-fail-on ns/op,allocs/op]")
		os.Exit(2)
	}

	baseFile, err := os.Open(*baseline)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kit-bench-gate: open baseline: %v\n", err)
		os.Exit(2)
	}
	defer baseFile.Close()
	curFile, err := os.Open(*current)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kit-bench-gate: open current: %v\n", err)
		os.Exit(2)
	}
	defer curFile.Close()

	baseResults, err := Parse(baseFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kit-bench-gate: parse baseline: %v\n", err)
		os.Exit(2)
	}
	curResults, err := Parse(curFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kit-bench-gate: parse current: %v\n", err)
		os.Exit(2)
	}

	failOn := parseFailOn(*failOnFlag)
	diffs := Compare(baseResults, curResults,
		[]Metric{MetricNs, MetricBytes, MetricAllocs},
		failOn,
		*threshold,
	)
	fmt.Print(Format(diffs))

	if HasRegressions(diffs) {
		os.Exit(1)
	}
}

func parseFailOn(s string) map[Metric]struct{} {
	out := make(map[Metric]struct{})
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		out[Metric(tok)] = struct{}{}
	}
	return out
}
