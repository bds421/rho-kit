package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestScanMetrics_ExtractsNameAndLabels(t *testing.T) {
	dir := t.TempDir()
	src := `package m

import "github.com/prometheus/client_golang/prometheus"

var _ = prometheus.NewCounterVec(
    prometheus.CounterOpts{Name: "http_requests_total", Help: "..."},
    []string{"method", "route", "status"},
)

var _ = prometheus.NewHistogramVec(
    prometheus.HistogramOpts{Name: "request_duration_seconds"},
    []string{"method"},
)
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := scanMetrics(dir)
	if err != nil {
		t.Fatalf("scanMetrics: %v", err)
	}

	requests, ok := out["http_requests_total"]
	if !ok {
		t.Fatalf("missing metric http_requests_total; got %v", keys(out))
	}
	got := setKeys(requests)
	want := []string{"method", "route", "status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("http_requests_total labels = %v, want %v", got, want)
	}

	dur, ok := out["request_duration_seconds"]
	if !ok {
		t.Fatalf("missing metric request_duration_seconds")
	}
	if got := setKeys(dur); !reflect.DeepEqual(got, []string{"method"}) {
		t.Fatalf("request_duration_seconds labels = %v, want [method]", got)
	}
}

func TestScanMetrics_IndeterminateLabelsRecordedAsNil(t *testing.T) {
	dir := t.TempDir()
	src := `package m

import "github.com/prometheus/client_golang/prometheus"

func build(labels []string) {
    _ = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "x"}, labels)
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := scanMetrics(dir)
	if err != nil {
		t.Fatalf("scanMetrics: %v", err)
	}
	labels, ok := out["x"]
	if !ok {
		t.Fatal("missing metric x")
	}
	if labels != nil {
		t.Fatalf("expected nil (indeterminate) labels; got %v", labels)
	}
}

func TestScanMetrics_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	src := `package m

import "github.com/prometheus/client_golang/prometheus"

var _ = prometheus.NewCounterVec(
    prometheus.CounterOpts{Name: "test_only_metric"},
    []string{"a"},
)
`
	if err := os.WriteFile(filepath.Join(dir, "thing_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := scanMetrics(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["test_only_metric"]; ok {
		t.Fatal("test files must be skipped")
	}
}

func TestParsePromQL_BasicSelector(t *testing.T) {
	refs := parsePromQL(`rate(http_requests_total{method="GET",route="/x"}[5m])`)
	want := []reference{
		{metric: "http_requests_total", label: "method"},
		{metric: "http_requests_total", label: "route"},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
}

func TestParsePromQL_RegexAndNegationOperators(t *testing.T) {
	refs := parsePromQL(`http_errors_total{status=~"5..", route!~"/health.*"}`)
	want := []reference{
		{metric: "http_errors_total", label: "status"},
		{metric: "http_errors_total", label: "route"},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
}

func TestParsePromQL_MultipleMetricsInOneExpression(t *testing.T) {
	refs := parsePromQL(`sum(rate(a_total{x="1"}[5m])) / sum(rate(b_total{y="2"}[5m]))`)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d (%v)", len(refs), refs)
	}
}

func TestParsePromQL_NoLabelsNoOutput(t *testing.T) {
	if refs := parsePromQL(`rate(no_labels_total[5m])`); len(refs) != 0 {
		t.Fatalf("expected no refs when no { block, got %v", refs)
	}
}

func TestVerify_DropsStandardLabels(t *testing.T) {
	labels := metricLabelSet{
		"x": {"a": {}},
	}
	refs := []reference{
		{metric: "x", label: "instance"}, // standard, allowed
		{metric: "x", label: "job"},      // standard, allowed
		{metric: "x", label: "a"},        // declared, allowed
	}
	if v := verify(labels, refs); len(v) != 0 {
		t.Fatalf("expected no violations; got %v", v)
	}
}

func TestVerify_FlagsUndeclaredLabel(t *testing.T) {
	labels := metricLabelSet{
		"x": {"a": {}},
	}
	refs := []reference{
		{metric: "x", label: "missing", dashboardFile: "x.json"},
	}
	v := verify(labels, refs)
	if len(v) != 1 {
		t.Fatalf("expected 1 violation, got %v", v)
	}
	if v[0].label != "missing" || v[0].metric != "x" {
		t.Fatalf("violation = %+v", v[0])
	}
}

func TestVerify_SkipsUnknownMetric(t *testing.T) {
	// Metrics not declared in Go (3rd-party, stdlib) are NOT a
	// label-drift problem — they're a name-drift problem, which
	// the older check-dashboard-metrics.sh covers.
	labels := metricLabelSet{}
	refs := []reference{
		{metric: "unknown_metric", label: "label", dashboardFile: "x.json"},
	}
	if v := verify(labels, refs); len(v) != 0 {
		t.Fatalf("expected no violation for unknown metric; got %v", v)
	}
}

func TestVerify_SkipsIndeterminateMetric(t *testing.T) {
	labels := metricLabelSet{
		"x": nil, // labels could not be inferred statically
	}
	refs := []reference{
		{metric: "x", label: "anything", dashboardFile: "x.json"},
	}
	if v := verify(labels, refs); len(v) != 0 {
		t.Fatalf("expected no violation when metric labels are indeterminate; got %v", v)
	}
}

func TestVerify_DeduplicatesByMetricLabelFile(t *testing.T) {
	labels := metricLabelSet{
		"x": {"a": {}},
	}
	refs := []reference{
		{metric: "x", label: "missing", dashboardFile: "x.json"},
		{metric: "x", label: "missing", dashboardFile: "x.json"},
		{metric: "x", label: "missing", dashboardFile: "y.json"},
	}
	v := verify(labels, refs)
	if len(v) != 2 {
		t.Fatalf("expected 2 unique violations, got %d (%v)", len(v), v)
	}
}

func TestExtractFromJSON_PullsEveryExprField(t *testing.T) {
	dir := t.TempDir()
	src := `{
        "panels": [
          {"targets": [{"expr": "rate(a{l=\"1\"}[5m])"}]},
          {"targets": [{"expr": "rate(b{m=\"2\"}[5m])"}]}
        ],
        "templating": {
          "list": [{"query": "label_values(c, l)"}]
        }
    }`
	path := filepath.Join(dir, "x.json")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	refs, err := extractFromJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	metrics := map[string]bool{}
	for _, r := range refs {
		metrics[r.metric] = true
	}
	if !metrics["a"] || !metrics["b"] {
		t.Fatalf("expected metrics a and b in refs; got %v", metrics)
	}
}

func TestExtractFromYAML_ExtractsExprLines(t *testing.T) {
	dir := t.TempDir()
	src := `groups:
- name: x
  rules:
  - alert: A
    expr: sum(rate(a{l="1"}[5m])) > 0
  - alert: B
    expr: "rate(b{m=\"2\"}[5m])"
`
	path := filepath.Join(dir, "x.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	refs, err := extractFromYAML(path)
	if err != nil {
		t.Fatal(err)
	}
	metrics := map[string]bool{}
	for _, r := range refs {
		metrics[r.metric] = true
	}
	if !metrics["a"] || !metrics["b"] {
		t.Fatalf("expected metrics a and b; got %v", metrics)
	}
}

func keys(m metricLabelSet) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func setKeys(s map[string]struct{}) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
