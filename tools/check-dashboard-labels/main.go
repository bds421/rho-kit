// Command check-dashboard-labels enforces that every label selector
// appearing inside an observability/dashboards/ PromQL expression
// references a label that the kit actually declares for that metric.
//
// Today's check-dashboard-metrics.sh proves the *name* of every
// metric referenced in a dashboard exists in Go source. This tool
// fills the gap at the *label* level: a Grafana panel asserting
// `kit_http_requests_total{method="GET"}` should fail CI if the
// underlying CounterVec was registered with labels {"verb"} instead.
//
// # Scope
//
// AST scan: walks every *.go file in the workspace and extracts the
// metric name + labels from any call shaped like
//
//	NewCounterVec(prometheus.CounterOpts{Name: "..."}, []string{"a", "b"})
//	NewHistogramVec(prometheus.HistogramOpts{Name: "..."}, []string{"a", "b"})
//	NewGaugeVec(prometheus.GaugeOpts{Name: "..."}, []string{"a", "b"})
//
// (function-name match, so it covers prometheus.New*Vec,
// promauto.New*Vec, and the kit's promutil.New*Vec.) Variadic label
// passes (e.g. `append(labels, x)`) cannot be tracked statically and
// are skipped silently — those metrics fall through to the
// "unrecognised metric" branch and the dashboard reference is
// allowed by default.
//
// Dashboard scan: pulls every string field from the dashboard JSON
// and every `expr:` value from the recording-rule YAMLs, then
// extracts metric+label tokens with a regex (`metric{lbl="..."}`).
//
// # Allowlist
//
// Built-in standard labels (instance, job, exported_*, …) are
// allowed against every metric.
//
// Exit codes:
//   0  no drift
//   1  drift detected
//   2  CLI / discovery failure
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() {
	dashboardsDir := flag.String("dashboards", "observability/dashboards", "root of dashboard JSON / YAML")
	repoRoot := flag.String("repo", ".", "repository root (must contain go.work)")
	flag.Parse()

	root, err := filepath.Abs(*repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve repo root:", err)
		os.Exit(2)
	}
	if _, err := os.Stat(filepath.Join(root, "go.work")); err != nil {
		fmt.Fprintln(os.Stderr, "expected go.work in repo root:", err)
		os.Exit(2)
	}

	metricLabels, err := scanMetrics(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan metrics:", err)
		os.Exit(2)
	}

	references, err := scanDashboards(filepath.Join(root, *dashboardsDir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan dashboards:", err)
		os.Exit(2)
	}

	violations := verify(metricLabels, references)
	if len(violations) == 0 {
		fmt.Printf("dashboard label check OK (%d metrics scanned, %d dashboard references checked)\n",
			len(metricLabels), len(references))
		return
	}

	fmt.Fprintln(os.Stderr, "dashboard label drift detected:")
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s — label %q not in declared set %v (file: %s)\n",
			v.metric, v.label, v.allowed, v.dashboardFile)
	}
	os.Exit(1)
}

// metric -> set of valid label names (declared at NewXxxVec call site).
type metricLabelSet map[string]map[string]struct{}

func scanMetrics(root string) (metricLabelSet, error) {
	out := metricLabelSet{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		return scanGoFile(path, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanGoFile(path string, out metricLabelSet) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		// Skip unparsable files — the goal is best-effort drift
		// detection, not a parser regression detector.
		return nil
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fnName := calleeName(call.Fun)
		if !isVecConstructor(fnName) {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		name := extractMetricName(call.Args[0])
		if name == "" {
			return true
		}
		labels := extractLabels(call.Args[1])
		if labels == nil {
			// Indeterminate label set (e.g. a non-literal slice).
			// Record the metric with a nil allowlist so downstream
			// verification skips it rather than flagging every
			// label as drift.
			if _, exists := out[name]; !exists {
				out[name] = nil
			}
			return true
		}
		merge(out, name, labels)
		return true
	})
	return nil
}

func calleeName(fn ast.Expr) string {
	switch v := fn.(type) {
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.Ident:
		return v.Name
	}
	return ""
}

func isVecConstructor(name string) bool {
	switch name {
	case "NewCounterVec", "NewHistogramVec", "NewGaugeVec", "NewSummaryVec":
		return true
	}
	return false
}

func extractMetricName(arg ast.Expr) string {
	// Common shape: prometheus.CounterOpts{Name: "metric_name", ...}
	composite, ok := arg.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	for _, elt := range composite.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Name" {
			continue
		}
		lit, ok := kv.Value.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		unq, err := strconvUnquote(lit.Value)
		if err == nil {
			return unq
		}
	}
	return ""
}

func extractLabels(arg ast.Expr) []string {
	composite, ok := arg.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	// []string{"a", "b"} — the composite literal's Type may be an
	// ArrayType or omitted (e.g. when passed to a []string param).
	out := []string{}
	for _, elt := range composite.Elts {
		lit, ok := elt.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			// Non-literal element — we cannot resolve the full
			// label set statically.
			return nil
		}
		unq, err := strconvUnquote(lit.Value)
		if err != nil {
			return nil
		}
		out = append(out, unq)
	}
	return out
}

// strconvUnquote is a stripped-down strconv.Unquote that only
// accepts double-quoted strings — the only form `NewCounterVec`
// labels use in practice. Avoids pulling in strconv at the cost
// of a few lines.
func strconvUnquote(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("not double-quoted")
	}
	// We don't care about escape interpretation for label names —
	// Prometheus label names cannot contain backslashes anyway.
	return s[1 : len(s)-1], nil
}

func merge(dest metricLabelSet, name string, labels []string) {
	if existing, ok := dest[name]; ok {
		if existing == nil {
			// Previously seen with indeterminate labels — keep
			// indeterminate so we don't lock to a partial set.
			return
		}
		for _, l := range labels {
			existing[l] = struct{}{}
		}
		return
	}
	set := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		set[l] = struct{}{}
	}
	dest[name] = set
}

// Dashboard reference: "metric X is selected with label L in file F".
type reference struct {
	metric        string
	label         string
	dashboardFile string
}

func scanDashboards(root string) ([]reference, error) {
	var out []reference
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".json"):
			refs, err := extractFromJSON(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			out = append(out, refs...)
		case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"):
			refs, err := extractFromYAML(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			out = append(out, refs...)
		}
		return nil
	})
	return out, err
}

func extractFromJSON(path string) ([]reference, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	exprs := collectStrings(doc, "expr")
	var refs []reference
	for _, e := range exprs {
		for _, r := range parsePromQL(e) {
			r.dashboardFile = path
			refs = append(refs, r)
		}
	}
	return refs, nil
}

func extractFromYAML(path string) ([]reference, error) {
	// Minimal YAML expr: lines of the form `expr: "..."` or
	// `expr: '...'` or `expr: rate(metric[1m])`. We avoid pulling
	// in yaml.v3 by line-scanning for `expr:`.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var refs []reference
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "expr:") {
			continue
		}
		expr := strings.TrimSpace(strings.TrimPrefix(trimmed, "expr:"))
		expr = strings.Trim(expr, `"'|>+-`)
		expr = strings.TrimSpace(expr)
		for _, r := range parsePromQL(expr) {
			r.dashboardFile = path
			refs = append(refs, r)
		}
	}
	return refs, nil
}

// collectStrings walks an arbitrary JSON-decoded value and returns
// every string-typed value at the named key. Used to harvest "expr"
// fields from Grafana panel JSON.
func collectStrings(v any, key string) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == key {
				if s, ok := val.(string); ok {
					out = append(out, s)
				}
			}
			out = append(out, collectStrings(val, key)...)
		}
	case []any:
		for _, item := range t {
			out = append(out, collectStrings(item, key)...)
		}
	}
	return out
}

// promqlSelectorRe extracts metric-name + label-set tokens from a
// PromQL expression. It deliberately does not parse PromQL — it
// just captures the {} block immediately following a metric name.
//
//	rate(kit_http_requests_total{method="GET",code="200"}[5m])
//	     ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
//
// Recording rules and aggregations can produce selectors without
// metric names (`{label=...}` in a recording-rule subquery); those
// rows are skipped — we cannot attribute their labels to a metric
// without a proper PromQL parser.
var promqlSelectorRe = regexp.MustCompile(`([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\}`)
var labelRe = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~?|!=|!~)`)

func parsePromQL(expr string) []reference {
	var refs []reference
	matches := promqlSelectorRe.FindAllStringSubmatch(expr, -1)
	for _, m := range matches {
		metric := m[1]
		labelBlock := m[2]
		for _, lm := range labelRe.FindAllStringSubmatch(labelBlock, -1) {
			refs = append(refs, reference{
				metric: metric,
				label:  lm[1],
			})
		}
	}
	return refs
}

// Standard scrape-time labels Prometheus attaches that are NOT
// declared in the Go NewXxxVec call but are valid to select on.
var standardLabels = map[string]struct{}{
	"instance":           {},
	"job":                {},
	"namespace":          {},
	"pod":                {},
	"container":          {},
	"node":               {},
	"service":            {},
	"cluster":            {},
	"app":                {},
	"environment":        {},
	"exported_instance":  {},
	"exported_namespace": {},
	"exported_service":   {},
	"exported_job":       {},
	"le":                 {}, // histogram bucket boundary
	"quantile":           {}, // summary quantile
}

type violation struct {
	metric        string
	label         string
	allowed       []string
	dashboardFile string
}

func verify(metricLabels metricLabelSet, refs []reference) []violation {
	// Deduplicate violations keyed by metric+label+file.
	type key struct{ m, l, f string }
	seen := map[key]struct{}{}
	var out []violation

	for _, r := range refs {
		if _, std := standardLabels[r.label]; std {
			continue
		}
		labels, known := metricLabels[r.metric]
		if !known {
			// Metric isn't in the kit's Go code — could be a
			// stdlib metric (go_, process_), a 3rd-party metric
			// (node_exporter, kube-state-metrics), or genuinely
			// drifted. Drift on names is the existing
			// check-dashboard-metrics.sh's job; we skip here.
			continue
		}
		if labels == nil {
			// Indeterminate label set; cannot judge.
			continue
		}
		if _, ok := labels[r.label]; ok {
			continue
		}
		k := key{r.metric, r.label, r.dashboardFile}
		if _, exists := seen[k]; exists {
			continue
		}
		seen[k] = struct{}{}
		allowed := make([]string, 0, len(labels))
		for l := range labels {
			allowed = append(allowed, l)
		}
		sort.Strings(allowed)
		out = append(out, violation{
			metric:        r.metric,
			label:         r.label,
			allowed:       allowed,
			dashboardFile: r.dashboardFile,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].dashboardFile != out[j].dashboardFile {
			return out[i].dashboardFile < out[j].dashboardFile
		}
		if out[i].metric != out[j].metric {
			return out[i].metric < out[j].metric
		}
		return out[i].label < out[j].label
	})
	return out
}
