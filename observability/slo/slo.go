package slo

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// SLOType categorises the kind of service level objective.
type SLOType string

const (
	// TypeLatency tracks request latency against a percentile threshold.
	TypeLatency SLOType = "latency"

	// TypeErrorRate tracks the ratio of failed requests.
	TypeErrorRate SLOType = "error_rate"

	// TypeSuccessRate tracks the ratio of successful requests (1 - error rate).
	// This measures success ratio from the service's own Prometheus counters
	// ("of the requests I handled, what percentage succeeded?"). It does NOT
	// measure true availability/uptime — if the service is down it records
	// nothing. True availability requires an external observer (load balancer
	// metrics, synthetic probes, Blackbox Exporter).
	TypeSuccessRate SLOType = "success_rate"
)

// SLO defines a single service level objective.
type SLO struct {
	// Name identifies this SLO (must be unique per Checker).
	Name string

	// Type is the category of the objective (latency, error_rate, success_rate).
	Type SLOType

	// Threshold is the target value. Interpretation depends on Type:
	//   - Latency: maximum acceptable seconds at the given Percentile.
	//   - ErrorRate: maximum acceptable error ratio (e.g. 0.001 for 0.1%).
	//   - SuccessRate: minimum acceptable success ratio (e.g. 0.999).
	Threshold float64

	// Percentile is used only for TypeLatency SLOs (e.g. 0.99 for p99).
	Percentile float64

	// Window is metadata describing the time window the SLO targets
	// (e.g. 7d for an availability SLO). The in-process [Checker] does
	// NOT enforce window-bounded math — it computes ratios over the
	// lifetime of process counters since startup. For true
	// window-bounded evaluation, query Prometheus directly with
	// rate(...) over the desired range; use this field for
	// dashboards/alerts to label the SLO context.
	Window time.Duration

	// MetricName overrides the default Prometheus metric name used for evaluation.
	// If empty, the Checker uses well-known defaults:
	//   - Latency: "http_request_duration_seconds"
	//   - ErrorRate / SuccessRate: "http_requests_total"
	MetricName string

	// ErrorLabelFilter specifies the label name and value that identifies error
	// responses. Only used for ErrorRate and SuccessRate types.
	// Defaults to status=~"5.." if empty (matches what
	// observability/redmetrics emits on http_requests_total).
	ErrorLabelFilter LabelFilter

	// LatencyLabelFilter restricts which histogram label combinations
	// contribute to the latency percentile. Used only for [TypeLatency].
	// Empty (Name == "") aggregates across all label combinations — the
	// legacy behaviour, which mixes p99 across e.g. all routes and
	// methods. Set Name+Pattern (or LabelMatcher) to scope the SLO to a
	// single endpoint or status class.
	LatencyLabelFilter LabelFilter
}

// LabelFilter defines a label name and a pattern for filtering metrics.
// The pattern uses '.' as a single-character wildcard (e.g. "5.." matches "500", "502").
type LabelFilter struct {
	Name    string
	Pattern string
}

// SLOStatus holds the evaluation result for a single SLO.
// The Window field is excluded from direct JSON encoding (json:"-") because
// time.Duration serialises as nanoseconds. Use the httpx/slohttp package for
// a JSON-friendly HTTP handler.
type SLOStatus struct {
	Name      string        `json:"name"`
	Type      SLOType       `json:"type"`
	Threshold float64       `json:"threshold"`
	Current   float64       `json:"current"`
	Breached  bool          `json:"breached"`
	BurnRate  float64       `json:"burn_rate"`
	Window    time.Duration `json:"-"`
}

// LatencySLO creates an SLO that tracks request latency at a given percentile.
// maxSeconds is the maximum acceptable latency in seconds at the specified percentile.
func LatencySLO(name string, percentile, maxSeconds float64, window time.Duration) SLO {
	return SLO{
		Name:       name,
		Type:       TypeLatency,
		Threshold:  maxSeconds,
		Percentile: percentile,
		Window:     window,
	}
}

// ErrorRateSLO creates an SLO that tracks the error rate.
// maxRate is the maximum acceptable error ratio (e.g. 0.001 for 0.1%).
func ErrorRateSLO(name string, maxRate float64, window time.Duration) SLO {
	return SLO{
		Name:      name,
		Type:      TypeErrorRate,
		Threshold: maxRate,
		Window:    window,
	}
}

// SuccessRateSLO creates an SLO that tracks the success rate (1 - error rate)
// from the service's own Prometheus counters. This measures "of the requests
// I handled, what percentage succeeded?" — it does NOT measure true
// availability. If the service is down, no metrics are recorded.
// minSuccessRate is the minimum acceptable success ratio (e.g. 0.999 for 99.9%).
func SuccessRateSLO(name string, minSuccessRate float64, window time.Duration) SLO {
	return SLO{
		Name:      name,
		Type:      TypeSuccessRate,
		Threshold: minSuccessRate,
		Window:    window,
	}
}

// Checker evaluates SLOs against a Prometheus Gatherer.
type Checker struct {
	gatherer prometheus.Gatherer
	slos     []SLO
}

// NewChecker creates a Checker that evaluates the given SLOs using metrics
// from gatherer. The SLOs slice is copied -- subsequent modifications to the
// caller's slice have no effect.
//
// Panics if gatherer is nil, if any SLO has an empty Name, or if duplicate
// SLO names are provided. These are configuration errors that should be caught
// at startup.
func NewChecker(gatherer prometheus.Gatherer, slos ...SLO) *Checker {
	if gatherer == nil {
		panic("slo: NewChecker gatherer must not be nil")
	}

	seen := make(map[string]struct{}, len(slos))
	for _, s := range slos {
		if s.Name == "" {
			panic("slo: SLO name must not be empty")
		}
		if _, exists := seen[s.Name]; exists {
			panic("slo: NewChecker duplicate SLO name")
		}
		switch s.Type {
		case TypeLatency:
			if s.Threshold <= 0 {
				panic("slo: NewChecker latency SLO Threshold must be > 0 (seconds at the configured percentile)")
			}
			if s.Percentile <= 0 || s.Percentile >= 1 {
				panic("slo: NewChecker latency SLO Percentile must lie in (0, 1)")
			}
		case TypeErrorRate:
			if s.Threshold < 0 || s.Threshold > 1 {
				panic("slo: NewChecker error-rate SLO Threshold must lie in [0, 1]")
			}
		case TypeSuccessRate:
			if s.Threshold < 0 || s.Threshold > 1 {
				panic("slo: NewChecker success-rate SLO Threshold must lie in [0, 1]")
			}
		default:
			panic("slo: SLO Type must be one of TypeLatency, TypeErrorRate, TypeSuccessRate")
		}
		if s.Window < 0 {
			panic("slo: SLO Window must not be negative")
		}
		seen[s.Name] = struct{}{}
	}

	copied := make([]SLO, len(slos))
	copy(copied, slos)
	return &Checker{
		gatherer: gatherer,
		slos:     copied,
	}
}

// Evaluate gathers current Prometheus metrics and evaluates every SLO,
// returning an SLOStatus for each. The returned slice order matches the
// order SLOs were provided to NewChecker.
func (c *Checker) Evaluate() []SLOStatus {
	families := c.gatherFamilies()

	statuses := make([]SLOStatus, 0, len(c.slos))
	for _, s := range c.slos {
		statuses = append(statuses, evaluateSLO(s, families))
	}
	return statuses
}

// gatherFamilies collects all metric families from the gatherer and returns
// them indexed by name for O(1) lookup. Prometheus Gather() may return partial
// results alongside errors; we use whatever data is available and log gather
// errors at warn level so operators can spot collector misconfiguration
// rather than silently degraded SLO evaluation (L-155).
func (c *Checker) gatherFamilies() map[string]*dto.MetricFamily {
	mfs, err := c.gatherer.Gather()
	if err != nil {
		slog.Warn("slo: prometheus gather returned errors; using partial data",
			redact.Error(err),
			"families", len(mfs),
		)
	}
	if len(mfs) == 0 {
		return make(map[string]*dto.MetricFamily)
	}

	result := make(map[string]*dto.MetricFamily, len(mfs))
	for _, mf := range mfs {
		result[mf.GetName()] = mf
	}
	return result
}

// evaluateSLO computes the status for a single SLO given gathered metric families.
func evaluateSLO(s SLO, families map[string]*dto.MetricFamily) SLOStatus {
	status := SLOStatus{
		Name:      s.Name,
		Type:      s.Type,
		Threshold: s.Threshold,
		Window:    s.Window,
		Current:   math.NaN(),
	}

	switch s.Type {
	case TypeLatency:
		status.Current = evaluateLatency(s, families)
	case TypeErrorRate:
		status.Current = evaluateErrorRate(s, families)
	case TypeSuccessRate:
		status.Current = evaluateSuccessRate(s, families)
	}

	if math.IsNaN(status.Current) {
		return status
	}

	status.Breached = isSLOBreached(s, status.Current)
	status.BurnRate = CalculateBurnRate(s, status.Current)

	return status
}

// isSLOBreached returns true when the current value violates the SLO threshold.
func isSLOBreached(s SLO, current float64) bool {
	switch s.Type {
	case TypeLatency, TypeErrorRate:
		return current > s.Threshold
	case TypeSuccessRate:
		return current < s.Threshold
	default:
		return false
	}
}

// defaultLatencyMetric is the default histogram name for latency SLOs.
const defaultLatencyMetric = "http_request_duration_seconds"

// defaultRequestMetric is the default counter name for error rate / success rate SLOs.
const defaultRequestMetric = "http_requests_total"

// evaluateLatency extracts the current percentile latency from a histogram metric.
func evaluateLatency(s SLO, families map[string]*dto.MetricFamily) float64 {
	metricName := s.MetricName
	if metricName == "" {
		metricName = defaultLatencyMetric
	}

	mf, ok := families[metricName]
	if !ok {
		return math.NaN()
	}

	if s.LatencyLabelFilter.Name != "" {
		mf = filterHistogramByLabel(mf, s.LatencyLabelFilter)
		if mf == nil {
			return math.NaN()
		}
	}

	return histogramPercentile(mf, s.Percentile)
}

// filterHistogramByLabel returns a shallow-copy [dto.MetricFamily]
// containing only the metrics whose labels match filter. Returns nil if no
// metrics match. The returned family is safe to pass to histogramPercentile,
// which aggregates across all metrics — restricting the input restricts the
// aggregation.
func filterHistogramByLabel(mf *dto.MetricFamily, filter LabelFilter) *dto.MetricFamily {
	src := mf.GetMetric()
	matched := make([]*dto.Metric, 0, len(src))
	for _, m := range src {
		if matchesLabel(m.GetLabel(), filter) {
			matched = append(matched, m)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	return &dto.MetricFamily{
		Name:   mf.Name,
		Help:   mf.Help,
		Type:   mf.Type,
		Metric: matched,
		Unit:   mf.Unit,
	}
}

// evaluateErrorRate computes the error ratio from a counter metric.
func evaluateErrorRate(s SLO, families map[string]*dto.MetricFamily) float64 {
	metricName := s.MetricName
	if metricName == "" {
		metricName = defaultRequestMetric
	}

	mf, ok := families[metricName]
	if !ok {
		return math.NaN()
	}

	errorFilter := s.ErrorLabelFilter
	if errorFilter.Name == "" {
		// Match the label that observability/redmetrics emits on
		// http_requests_total. Earlier versions used "code", but no
		// kit-emitted metric carries that label, so the default SLO
		// silently always returned 0% errors.
		errorFilter = LabelFilter{Name: "status", Pattern: "5.."}
	}

	total, errors := sumCountersByLabel(mf, errorFilter)
	if total == 0 {
		return math.NaN()
	}

	return errors / total
}

// evaluateSuccessRate computes the success ratio (1 - error rate).
func evaluateSuccessRate(s SLO, families map[string]*dto.MetricFamily) float64 {
	errorRate := evaluateErrorRate(SLO{
		Name:             s.Name,
		MetricName:       s.MetricName,
		ErrorLabelFilter: s.ErrorLabelFilter,
		Type:             TypeErrorRate,
		Window:           s.Window,
	}, families)

	if math.IsNaN(errorRate) {
		return math.NaN()
	}

	return 1 - errorRate
}

// histogramPercentile computes an approximate percentile from histogram buckets
// using linear interpolation. This mirrors how Prometheus' histogram_quantile works.
func histogramPercentile(mf *dto.MetricFamily, percentile float64) float64 {
	if mf.GetType() != dto.MetricType_HISTOGRAM {
		return math.NaN()
	}

	metrics := mf.GetMetric()
	if len(metrics) == 0 {
		return math.NaN()
	}

	// Aggregate across all label combinations.
	var totalCount uint64
	bucketMap := make(map[float64]uint64)

	for _, m := range metrics {
		h := m.GetHistogram()
		if h == nil {
			continue
		}
		for _, b := range h.GetBucket() {
			bucketMap[b.GetUpperBound()] += b.GetCumulativeCount()
		}
		totalCount += h.GetSampleCount()
	}

	if totalCount == 0 {
		return math.NaN()
	}

	buckets := sortedBuckets(bucketMap)

	// Enforce monotonic non-decreasing cumulative counts. When metrics
	// in the same family use mismatched bucket boundaries (which the
	// Prometheus convention forbids but a custom registration could
	// produce), summing CumulativeCount per upper-bound gives a sparse
	// distribution that breaks linear interpolation. Walk in ascending
	// bound order and propagate max(prev, current) so the cumulative
	// invariant is restored before the percentile loop runs.
	for i := 1; i < len(buckets); i++ {
		if buckets[i].cumulativeCount < buckets[i-1].cumulativeCount {
			buckets[i].cumulativeCount = buckets[i-1].cumulativeCount
		}
	}

	target := percentile * float64(totalCount)
	var prevCount float64
	var prevBound float64

	for _, b := range buckets {
		if float64(b.cumulativeCount) >= target {
			// Linear interpolation within the bucket.
			bucketWidth := b.upperBound - prevBound
			countInBucket := float64(b.cumulativeCount) - prevCount
			if countInBucket == 0 {
				return prevBound
			}
			fraction := (target - prevCount) / countInBucket
			return prevBound + fraction*bucketWidth
		}
		prevCount = float64(b.cumulativeCount)
		prevBound = b.upperBound
	}

	// Target lies in the +Inf bucket — i.e. samples exceed every finite
	// bucket's upper bound. Return +Inf so callers comparing against a
	// latency threshold correctly observe a breach. Earlier versions
	// returned the largest finite bound, which silently underreported
	// tail-latency violations.
	if len(buckets) > 0 {
		return math.Inf(1)
	}
	return math.NaN()
}

type sortableBucket struct {
	upperBound      float64
	cumulativeCount uint64
}

// sortedBuckets converts a map of upper_bound->count into a slice sorted by upper bound.
// The +Inf bucket is excluded because it is not useful for percentile interpolation.
func sortedBuckets(m map[float64]uint64) []sortableBucket {
	buckets := make([]sortableBucket, 0, len(m))
	for ub, count := range m {
		if math.IsInf(ub, 1) {
			continue // skip +Inf bucket for interpolation
		}
		buckets = append(buckets, sortableBucket{upperBound: ub, cumulativeCount: count})
	}

	slices.SortFunc(buckets, func(a, b sortableBucket) int {
		switch {
		case a.upperBound < b.upperBound:
			return -1
		case a.upperBound > b.upperBound:
			return 1
		default:
			return 0
		}
	})
	return buckets
}

// sumCountersByLabel sums counter values, partitioning into total and matching
// (error) counts based on the label filter.
func sumCountersByLabel(mf *dto.MetricFamily, filter LabelFilter) (total, matched float64) {
	if mf.GetType() != dto.MetricType_COUNTER {
		return 0, 0
	}

	for _, m := range mf.GetMetric() {
		val := m.GetCounter().GetValue()
		total += val

		if matchesLabel(m.GetLabel(), filter) {
			matched += val
		}
	}
	return total, matched
}

// matchesLabel checks if any label on the metric matches the filter.
func matchesLabel(labels []*dto.LabelPair, filter LabelFilter) bool {
	for _, lp := range labels {
		if lp.GetName() != filter.Name {
			continue
		}
		if matchPattern(filter.Pattern, lp.GetValue()) {
			return true
		}
	}
	return false
}

// matchPattern performs a fixed-length pattern match where '.' matches any
// single character. For example, "5.." matches "500" and "502" but not "50"
// or "5000".
func matchPattern(pattern, value string) bool {
	if len(pattern) != len(value) {
		return false
	}
	for i := range pattern {
		if pattern[i] != '.' && pattern[i] != value[i] {
			return false
		}
	}
	return true
}

// DependencyCheck returns a health.DependencyCheck that reports "degraded" when
// any SLO is breached. It is configured as non-critical: an SLO breach should
// not make the service unready (killing traffic to an already-struggling service
// makes things worse). The check is informational — use the /slo endpoint for
// dashboards and alerting.
func (c *Checker) DependencyCheck() health.DependencyCheck {
	return health.DependencyCheck{
		Name:     "slo",
		Critical: false,
		Check: func(_ context.Context) string {
			for _, s := range c.Evaluate() {
				if s.Breached {
					return health.StatusDegraded
				}
			}
			return health.StatusHealthy
		},
	}
}

// String returns a human-readable summary of the SLO status.
func (s SLOStatus) String() string {
	state := "OK"
	if s.Breached {
		state = "BREACHED"
	}
	return fmt.Sprintf("[%s] %s: current=%.6f threshold=%.6f burn_rate=%.2f (%s)",
		s.Type, s.Name, s.Current, s.Threshold, s.BurnRate, state)
}
