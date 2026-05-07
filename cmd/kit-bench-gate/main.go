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
	"errors"
	"flag"
	"fmt"
	"io"
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

	failOn, err := parseFailOn(*failOnFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kit-bench-gate: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: kit-bench-gate -baseline FILE -current FILE [-threshold 10] [-fail-on ns/op,allocs/op]")
		os.Exit(2)
	}

	code, err := run(*baseline, *current, failOn, *threshold, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kit-bench-gate: %v\n", err)
	}
	os.Exit(code)
}

func run(baselinePath, currentPath string, failOn map[Metric]struct{}, threshold float64, out io.Writer) (code int, err error) {
	baseResults, err := readResults(baselinePath, "baseline")
	if err != nil {
		return 2, err
	}
	curResults, err := readResults(currentPath, "current")
	if err != nil {
		return 2, err
	}

	diffs := Compare(baseResults, curResults,
		[]Metric{MetricNs, MetricBytes, MetricAllocs},
		failOn,
		threshold,
	)
	if _, werr := fmt.Fprint(out, Format(diffs)); werr != nil {
		return 2, fmt.Errorf("write output: %w", werr)
	}

	if HasRegressions(diffs) {
		return 1, nil
	}
	return 0, nil
}

func readResults(path, label string) (results []Result, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", label, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			cerr = fmt.Errorf("close %s: %w", label, cerr)
			if err == nil {
				err = cerr
			} else {
				err = errors.Join(err, cerr)
			}
		}
	}()

	results, err = Parse(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", label, err)
	}
	return results, nil
}

func parseFailOn(s string) (map[Metric]struct{}, error) {
	out := make(map[Metric]struct{})
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		m := Metric(tok)
		if !IsValidMetric(m) {
			return nil, fmt.Errorf("unknown -fail-on metric %q (valid: %s)",
				tok, strings.Join(metricNames(), ","))
		}
		out[m] = struct{}{}
	}
	return out, nil
}

func metricNames() []string {
	out := make([]string, 0, len(SupportedMetrics))
	for _, m := range SupportedMetrics {
		out = append(out, string(m))
	}
	return out
}
