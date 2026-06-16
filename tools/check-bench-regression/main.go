// Command check-bench-regression runs the kit's hot-path
// benchmarks and compares results to a checked-in baseline.
//
// # Why this exists
//
// Per-call cost of helpers like redact.WrapError or
// promutil.OpaqueLabelValue compounds across every kit-emitted
// log line / metric label. A 50ns regression in WrapError is a
// 50% regression in services that log densely. Catching this
// before tag means the tag's baseline is also the tag's commitment
// to consumers.
//
// # How it works
//
//  1. Discovers every benchmark in the configured target packages.
//  2. Runs them with -count=COUNT -benchmem -benchtime=N (default
//     3 runs × 1s) and parses the `go test -bench` output.
//  3. Picks the BEST (lowest ns/op) run per benchmark — multi-run
//     averaging is left for the operator's local workflow; CI
//     prefers a stable lower bound.
//  4. Compares each benchmark against the value in
//     benchmarks-baseline.txt. Failure threshold:
//     ns/op > baseline_ns * tolerance  (default 1.25 — 25% regression)
//     OR
//     allocs/op > baseline_allocs * 2  (alloc count doubling is loud)
//  5. Exit 1 on any regression with a diff-friendly report.
//
// # Updating the baseline
//
// When a known-acceptable change moves a number (refactor, etc.),
// re-run with -update to overwrite the baseline:
//
//	tools/check-bench-regression.sh -update
//
// Commit the updated benchmarks-baseline.txt alongside the change.
//
// # Output format
//
// Baseline file format (one benchmark per line):
//
//	BenchmarkName  ns_per_op  allocs_per_op  bytes_per_op
//
// e.g.
//
//	BenchmarkWrapError 145 1 48
//
// Comments (#) and blank lines are ignored.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type benchResult struct {
	Name    string
	NsPerOp float64
	Allocs  int64
	Bytes   int64
}

type config struct {
	repoRoot  string
	baseline  string
	tolerance float64
	count     int
	benchTime string
	update    bool
	pkgsArg   string
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.repoRoot, "root", ".", "Repository root.")
	flag.StringVar(&cfg.baseline, "baseline", "tools/check-bench-regression/benchmarks-baseline.txt", "Path to baseline file (relative to repo root).")
	flag.Float64Var(&cfg.tolerance, "tolerance", 1.25, "Allowed regression multiplier on ns/op (1.25 = 25% slower fails).")
	flag.IntVar(&cfg.count, "count", 3, "go test -count value (best run wins).")
	flag.StringVar(&cfg.benchTime, "benchtime", "1s", "go test -benchtime value.")
	flag.BoolVar(&cfg.update, "update", false, "Overwrite the baseline with the just-measured values.")
	flag.StringVar(&cfg.pkgsArg, "pkgs", "./core/redact/...,./observability/promutil/...,./httpx/websocket/...", "Comma-separated package patterns to benchmark.")
	flag.Parse()

	root, err := filepath.Abs(cfg.repoRoot)
	if err != nil {
		fail("resolve root: %v", err)
	}
	cfg.repoRoot = root

	pkgs := strings.Split(cfg.pkgsArg, ",")
	results, err := runBenchmarks(cfg, pkgs)
	if err != nil {
		fail("run benchmarks: %v", err)
	}
	if len(results) == 0 {
		fail("no benchmarks found in target packages")
	}

	if cfg.update {
		if err := writeBaseline(filepath.Join(cfg.repoRoot, cfg.baseline), results); err != nil {
			fail("write baseline: %v", err)
		}
		fmt.Printf("benchmarks-baseline.txt updated with %d entries.\n", len(results))
		return
	}

	baseline, err := readBaseline(filepath.Join(cfg.repoRoot, cfg.baseline))
	if err != nil {
		fail("read baseline: %v", err)
	}

	type finding struct {
		name string
		text string
	}
	var findings []finding
	for _, r := range results {
		base, ok := baseline[r.Name]
		if !ok {
			findings = append(findings, finding{r.Name, fmt.Sprintf("%s: NOT in baseline (current %.0f ns/op, %d allocs, %d B/op) — re-run with -update if intentional", r.Name, r.NsPerOp, r.Allocs, r.Bytes)})
			continue
		}
		if base.NsPerOp > 0 && r.NsPerOp > base.NsPerOp*cfg.tolerance {
			delta := (r.NsPerOp/base.NsPerOp - 1.0) * 100
			findings = append(findings, finding{r.Name, fmt.Sprintf("%s: ns/op regressed from %.0f to %.0f (+%.1f%%; tolerance %.0f%%)", r.Name, base.NsPerOp, r.NsPerOp, delta, (cfg.tolerance-1.0)*100)})
		}
		if base.Allocs >= 0 && r.Allocs > base.Allocs*2 && r.Allocs > base.Allocs+1 {
			findings = append(findings, finding{r.Name, fmt.Sprintf("%s: allocs/op regressed from %d to %d", r.Name, base.Allocs, r.Allocs)})
		}
	}

	if len(findings) == 0 {
		fmt.Printf("bench-regression OK (%d benchmarks within tolerance)\n", len(results))
		return
	}

	sort.Slice(findings, func(i, j int) bool { return findings[i].name < findings[j].name })
	fmt.Fprintln(os.Stderr, "bench-regression FAILED")
	fmt.Fprintln(os.Stderr)
	for _, f := range findings {
		fmt.Fprintf(os.Stderr, "  %s\n", f.text)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "To accept the new baseline, run: tools/check-bench-regression.sh -update")
	os.Exit(1)
}

func runBenchmarks(cfg config, pkgs []string) ([]benchResult, error) {
	args := []string{"test", "-run", "^$", "-bench", ".", "-benchmem", "-benchtime", cfg.benchTime, "-count", strconv.Itoa(cfg.count)}
	args = append(args, pkgs...)
	cmd := exec.Command("go", args...)
	cmd.Dir = cfg.repoRoot
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go test: %w", err)
	}
	return parseBenchOutput(string(out)), nil
}

// parseBenchOutput extracts the best (lowest ns/op) result per
// benchmark name from the `go test -bench` output. The output
// format is documented at https://pkg.go.dev/testing — each
// benchmark line looks like:
//
//	BenchmarkName-N    iterations    X ns/op    Y B/op    Z allocs/op
//
// We collapse the trailing `-N` (GOMAXPROCS suffix) so the same
// benchmark across multiple -count runs maps to one entry.
func parseBenchOutput(out string) []benchResult {
	best := map[string]benchResult{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
			continue
		}
		name := stripGOMAXPROCSSuffix(fields[0])
		// Find "ns/op" and read the preceding number.
		var nsPerOp float64
		var allocs, bytes int64
		var haveNs bool
		for i := 1; i < len(fields)-1; i++ {
			switch fields[i+1] {
			case "ns/op":
				v, err := strconv.ParseFloat(fields[i], 64)
				if err == nil {
					nsPerOp = v
					haveNs = true
				}
			case "B/op":
				v, err := strconv.ParseInt(fields[i], 10, 64)
				if err == nil {
					bytes = v
				}
			case "allocs/op":
				v, err := strconv.ParseInt(fields[i], 10, 64)
				if err == nil {
					allocs = v
				}
			}
		}
		// Skip lines that carry no completed ns/op measurement (e.g. a
		// benchmark header without results). A genuine sub-1ns benchmark
		// reports "0.0000 ns/op"; that is a real measurement and must be
		// retained so it reaches the baseline comparison instead of
		// silently vanishing from results (and never tripping the
		// "NOT in baseline" notice).
		if !haveNs {
			continue
		}
		r := benchResult{Name: name, NsPerOp: nsPerOp, Allocs: allocs, Bytes: bytes}
		prev, exists := best[name]
		if !exists || r.NsPerOp < prev.NsPerOp {
			best[name] = r
		}
	}
	out2 := make([]benchResult, 0, len(best))
	for _, v := range best {
		out2 = append(out2, v)
	}
	sort.Slice(out2, func(i, j int) bool { return out2[i].Name < out2[j].Name })
	return out2
}

func stripGOMAXPROCSSuffix(name string) string {
	if i := strings.LastIndex(name, "-"); i > 0 {
		rest := name[i+1:]
		if _, err := strconv.Atoi(rest); err == nil {
			return name[:i]
		}
	}
	return name
}

func readBaseline(path string) (map[string]benchResult, error) {
	out := map[string]benchResult{}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return nil, fmt.Errorf("baseline line malformed: %q (expected: name ns allocs bytes)", line)
		}
		ns, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return nil, fmt.Errorf("baseline ns/op parse: %w", err)
		}
		allocs, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("baseline allocs parse: %w", err)
		}
		bytes, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("baseline bytes parse: %w", err)
		}
		out[fields[0]] = benchResult{Name: fields[0], NsPerOp: ns, Allocs: allocs, Bytes: bytes}
	}
	return out, scanner.Err()
}

func writeBaseline(path string, results []benchResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintln(f, "# rho-kit hot-path benchmark baseline (wave 175). One line per benchmark:")
	fmt.Fprintln(f, "#   <BenchmarkName>  <ns_per_op>  <allocs_per_op>  <bytes_per_op>")
	fmt.Fprintln(f, "# Re-run with `tools/check-bench-regression.sh -update` after intentional perf changes.")
	fmt.Fprintln(f, "")
	for _, r := range results {
		fmt.Fprintf(f, "%s %.0f %d %d\n", r.Name, r.NsPerOp, r.Allocs, r.Bytes)
	}
	return nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "check-bench-regression: "+format+"\n", args...)
	os.Exit(2)
}
