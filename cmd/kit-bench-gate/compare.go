package main

import (
	"fmt"
	"sort"
	"strings"
)

// Metric identifies which dimension of a benchmark is being compared.
type Metric string

const (
	MetricNs     Metric = "ns/op"
	MetricBytes  Metric = "B/op"
	MetricAllocs Metric = "allocs/op"
)

// Diff is one benchmark's regression report for a single metric.
type Diff struct {
	Name         string
	Metric       Metric
	Baseline     float64
	Current      float64
	PctChange    float64 // (current - baseline) / baseline * 100
	Regressed    bool    // true when PctChange exceeds the configured threshold
	NewBench     bool    // present in current but not baseline
	MissingBench bool    // present in baseline but absent in current
}

// Compare aligns baseline against current by name and produces a Diff
// per (benchmark, metric) pair tracked in `metrics`. A Diff is marked
// Regressed when its PctChange exceeds thresholdPct AND the metric
// appears in `failOn`.
func Compare(baseline, current []Result, metrics []Metric, failOn map[Metric]struct{}, thresholdPct float64) []Diff {
	baseByName := indexByName(baseline)
	curByName := indexByName(current)

	var out []Diff
	seen := make(map[string]bool, len(baseByName))
	for _, m := range metrics {
		for name, b := range baseByName {
			seen[name] = true
			c, ok := curByName[name]
			if !ok {
				out = append(out, Diff{Name: name, Metric: m, Baseline: getMetric(b, m), MissingBench: true})
				continue
			}
			out = append(out, mkDiff(name, m, b, c, failOn, thresholdPct))
		}
		for name, c := range curByName {
			if seen[name] {
				continue
			}
			out = append(out, Diff{Name: name, Metric: m, Current: getMetric(c, m), NewBench: true})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}

func indexByName(rs []Result) map[string]Result {
	m := make(map[string]Result, len(rs))
	for _, r := range rs {
		m[r.Name] = r
	}
	return m
}

func getMetric(r Result, m Metric) float64 {
	switch m {
	case MetricNs:
		return r.NsPerOp
	case MetricBytes:
		return float64(r.BPerOp)
	case MetricAllocs:
		return float64(r.AllocsOp)
	}
	return 0
}

func mkDiff(name string, m Metric, b, c Result, failOn map[Metric]struct{}, thresholdPct float64) Diff {
	bv := getMetric(b, m)
	cv := getMetric(c, m)
	d := Diff{Name: name, Metric: m, Baseline: bv, Current: cv}
	if bv > 0 {
		d.PctChange = (cv - bv) / bv * 100
	}
	if _, track := failOn[m]; track && d.PctChange > thresholdPct {
		d.Regressed = true
	}
	return d
}

// Format renders the diffs as a markdown-friendly table.
func Format(diffs []Diff) string {
	if len(diffs) == 0 {
		return "no benchmarks compared\n"
	}
	var b strings.Builder
	fmt.Fprintln(&b, "| benchmark | metric | baseline | current | Δ% | status |")
	fmt.Fprintln(&b, "|---|---|---:|---:|---:|---|")
	for _, d := range diffs {
		status := "ok"
		switch {
		case d.Regressed:
			status = "REGRESSED"
		case d.NewBench:
			status = "new"
		case d.MissingBench:
			status = "missing"
		}
		fmt.Fprintf(&b, "| %s | %s | %.2f | %.2f | %+.1f%% | %s |\n",
			d.Name, d.Metric, d.Baseline, d.Current, d.PctChange, status)
	}
	return b.String()
}

// HasRegressions reports whether any diff is marked as regressed.
func HasRegressions(diffs []Diff) bool {
	for _, d := range diffs {
		if d.Regressed {
			return true
		}
	}
	return false
}
