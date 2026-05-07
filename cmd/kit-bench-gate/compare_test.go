package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompare_RegressionMarkedWhenAboveThresholdAndTracked(t *testing.T) {
	base := []Result{{Name: "BenchmarkA", NsPerOp: 100, BPerOp: 64, AllocsOp: 2}}
	cur := []Result{{Name: "BenchmarkA", NsPerOp: 130, BPerOp: 64, AllocsOp: 2}}
	failOn := map[Metric]struct{}{MetricNs: {}}

	diffs := Compare(base, cur, []Metric{MetricNs, MetricBytes}, failOn, 10)

	var nsDiff, bytesDiff Diff
	for _, d := range diffs {
		switch d.Metric {
		case MetricNs:
			nsDiff = d
		case MetricBytes:
			bytesDiff = d
		}
	}
	assert.True(t, nsDiff.Regressed, "ns/op above threshold and in fail-on must be marked Regressed")
	assert.False(t, bytesDiff.Regressed, "B/op unchanged must not regress")
}

func TestCompare_RegressionInUntrackedMetricNotMarked(t *testing.T) {
	base := []Result{{Name: "BenchmarkA", NsPerOp: 100, BPerOp: 64}}
	cur := []Result{{Name: "BenchmarkA", NsPerOp: 100, BPerOp: 1000}}
	failOn := map[Metric]struct{}{MetricNs: {}} // B/op is NOT tracked

	diffs := Compare(base, cur, []Metric{MetricNs, MetricBytes}, failOn, 10)
	for _, d := range diffs {
		assert.False(t, d.Regressed, "B/op must not fail when not in fail-on, got %+v", d)
	}
}

func TestCompare_NewBenchFlaggedNew(t *testing.T) {
	base := []Result{{Name: "Existing", NsPerOp: 100}}
	cur := []Result{
		{Name: "Existing", NsPerOp: 100},
		{Name: "Brand-New", NsPerOp: 50},
	}
	diffs := Compare(base, cur, []Metric{MetricNs}, map[Metric]struct{}{MetricNs: {}}, 10)
	var found bool
	for _, d := range diffs {
		if d.Name == "Brand-New" && d.NewBench {
			found = true
		}
	}
	assert.True(t, found, "new benchmarks present in current but not baseline must be flagged")
}

func TestCompare_MissingBenchFlaggedMissing(t *testing.T) {
	base := []Result{{Name: "Gone", NsPerOp: 200}}
	cur := []Result{}
	diffs := Compare(base, cur, []Metric{MetricNs}, map[Metric]struct{}{MetricNs: {}}, 10)
	var found bool
	for _, d := range diffs {
		if d.Name == "Gone" && d.MissingBench {
			found = true
		}
	}
	assert.True(t, found, "benchmarks gone from current must be flagged missing")
}

func TestCompare_ZeroBaselineFlaggedAsRegression(t *testing.T) {
	base := []Result{{Name: "BenchmarkAlloc", AllocsOp: 0}}
	cur := []Result{{Name: "BenchmarkAlloc", AllocsOp: 1}}
	failOn := map[Metric]struct{}{MetricAllocs: {}}

	diffs := Compare(base, cur, []Metric{MetricAllocs}, failOn, 10)
	require.Len(t, diffs, 1)

	d := diffs[0]
	assert.True(t, d.Regressed, "0 -> positive must be marked Regressed")
	assert.True(t, d.ZeroBaseline, "ZeroBaseline must be true when baseline is zero and current is positive")
	assert.Equal(t, 0.0, d.PctChange, "PctChange must remain zero when baseline is zero")
	assert.Equal(t, 1.0, d.AbsoluteIncrease)

	out := Format(diffs)
	assert.Contains(t, out, "regression from zero")
	assert.Contains(t, out, "n/a")
}

func TestCompare_ZeroBaselineUntrackedNotMarked(t *testing.T) {
	base := []Result{{Name: "BenchmarkAlloc", AllocsOp: 0}}
	cur := []Result{{Name: "BenchmarkAlloc", AllocsOp: 1}}

	diffs := Compare(base, cur, []Metric{MetricAllocs}, map[Metric]struct{}{}, 10)
	require.Len(t, diffs, 1)
	assert.False(t, diffs[0].Regressed, "untracked metric must not regress even on zero baseline")
	assert.True(t, diffs[0].ZeroBaseline)
}

func TestCompare_ZeroBaselineZeroCurrentNotRegressed(t *testing.T) {
	base := []Result{{Name: "BenchmarkAlloc", AllocsOp: 0}}
	cur := []Result{{Name: "BenchmarkAlloc", AllocsOp: 0}}
	failOn := map[Metric]struct{}{MetricAllocs: {}}

	diffs := Compare(base, cur, []Metric{MetricAllocs}, failOn, 10)
	require.Len(t, diffs, 1)
	assert.False(t, diffs[0].Regressed)
	assert.False(t, diffs[0].ZeroBaseline)
}

func TestHasRegressions(t *testing.T) {
	assert.False(t, HasRegressions(nil))
	assert.False(t, HasRegressions([]Diff{{Regressed: false}}))
	assert.True(t, HasRegressions([]Diff{{Regressed: false}, {Regressed: true}}))
}

func TestFormat_RendersTable(t *testing.T) {
	out := Format([]Diff{
		{Name: "BenchmarkA", Metric: MetricNs, Baseline: 100, Current: 130, PctChange: 30, Regressed: true},
	})
	assert.Contains(t, out, "BenchmarkA")
	assert.Contains(t, out, "REGRESSED")
}
