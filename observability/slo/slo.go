package slo

import (
	"fmt"
	"math"
	"time"

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

	// TypeAvailability tracks the ratio of successful requests (1 - error rate).
	TypeAvailability SLOType = "availability"
)

// SLO defines a single service level objective.
type SLO struct {
	// Name identifies this SLO (must be unique per Checker).
	Name string

	// Type is the category of the objective (latency, error_rate, availability).
	Type SLOType

	// Threshold is the target value. Interpretation depends on Type:
	//   - Latency: maximum acceptable seconds at the given Percentile.
	//   - ErrorRate: maximum acceptable error ratio (e.g. 0.001 for 0.1%).
	//   - Availability: minimum acceptable success ratio (e.g. 0.999).
	Threshold float64

	// Percentile is used only for TypeLatency SLOs (e.g. 0.99 for p99).
	Percentile float64

	// Window is the evaluation time window.
	Window time.Duration

	// MetricName overrides the default Prometheus metric name used for evaluation.
	// If empty, the Checker uses well-known defaults:
	//   - Latency: "http_request_duration_seconds"
	//   - ErrorRate / Availability: "http_requests_total"
	MetricName string

	// ErrorLabelFilter specifies the label name and value that identifies error
	// responses. Only used for ErrorRate and Availability types.
	// Defaults to code=~"5.." if empty.
	ErrorLabelFilter LabelFilter
}

// LabelFilter defines a label name and regex pattern for filtering metrics.
type LabelFilter struct {
	Name    string
	Pattern string
}

// SLOStatus holds the evaluation result for a single SLO.
type SLOStatus struct {
	Name      string        `json:"name"`
	Type      SLOType       `json:"type"`
	Threshold float64       `json:"threshold"`
	Current   float64       `json:"current"`
	Breached  bool          `json:"breached"`
	BurnRate  float64       `json:"burn_rate"`
	Window    time.Duration `json:"window"`
}

// HTTPLatencySLO creates an SLO that tracks HTTP request latency at a given percentile.
// maxSeconds is the maximum acceptable latency in seconds at the specified percentile.
func HTTPLatencySLO(name string, percentile, maxSeconds float64, window time.Duration) SLO {
	return SLO{
		Name:       name,
		Type:       TypeLatency,
		Threshold:  maxSeconds,
		Percentile: percentile,
		Window:     window,
	}
}

// HTTPErrorRateSLO creates an SLO that tracks the HTTP error rate.
// maxRate is the maximum acceptable error ratio (e.g. 0.001 for 0.1%).
func HTTPErrorRateSLO(name string, maxRate float64, window time.Duration) SLO {
	return SLO{
		Name:      name,
		Type:      TypeErrorRate,
		Threshold: maxRate,
		Window:    window,
	}
}

// HTTPAvailabilitySLO creates an SLO that tracks HTTP availability.
// minAvailability is the minimum acceptable success ratio (e.g. 0.999 for 99.9%).
func HTTPAvailabilitySLO(name string, minAvailability float64, window time.Duration) SLO {
	return SLO{
		Name:      name,
		Type:      TypeAvailability,
		Threshold: minAvailability,
		Window:    window,
	}
}

// Checker evaluates SLOs against a Prometheus Gatherer.
type Checker struct {
	gatherer prometheus.Gatherer
	slos     []SLO
}

// NewChecker creates a Checker that evaluates the given SLOs using metrics
// from gatherer. The SLOs slice is copied — subsequent modifications to the
// caller's slice have no effect.
func NewChecker(gatherer prometheus.Gatherer, slos ...SLO) *Checker {
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
// them indexed by name for O(1) lookup.
func (c *Checker) gatherFamilies() map[string]*dto.MetricFamily {
	mfs, err := c.gatherer.Gather()
	if err != nil {
		// On gather error, return empty map — all SLOs will report NaN current
		// values and will not be marked as breached (no data != breach).
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
	case TypeAvailability:
		status.Current = evaluateAvailability(s, families)
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
	case TypeLatency:
		return current > s.Threshold
	case TypeErrorRate:
		return current > s.Threshold
	case TypeAvailability:
		return current < s.Threshold
	default:
		return false
	}
}

// defaultLatencyMetric is the default histogram name for latency SLOs.
const defaultLatencyMetric = "http_request_duration_seconds"

// defaultRequestMetric is the default counter name for error rate / availability SLOs.
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

	return histogramPercentile(mf, s.Percentile)
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
		errorFilter = LabelFilter{Name: "code", Pattern: "5.."}
	}

	total, errors := sumCountersByLabel(mf, errorFilter)
	if total == 0 {
		return math.NaN()
	}

	return errors / total
}

// evaluateAvailability computes the success ratio (1 - error rate).
func evaluateAvailability(s SLO, families map[string]*dto.MetricFamily) float64 {
	errorRate := evaluateErrorRate(SLO{
		MetricName:       s.MetricName,
		ErrorLabelFilter: s.ErrorLabelFilter,
		Type:             TypeErrorRate,
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
	type bucket struct {
		upperBound      float64
		cumulativeCount uint64
	}

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

	// Sort buckets by upper bound.
	buckets := sortBuckets(bucketMap)

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

	// If we didn't find a bucket, return the last upper bound.
	if len(buckets) > 0 {
		return buckets[len(buckets)-1].upperBound
	}
	return math.NaN()
}

type sortableBucket struct {
	upperBound      float64
	cumulativeCount uint64
}

// sortBuckets converts a map of upper_bound->count into a sorted slice.
func sortBuckets(m map[float64]uint64) []sortableBucket {
	buckets := make([]sortableBucket, 0, len(m))
	for ub, count := range m {
		if math.IsInf(ub, 1) {
			continue // skip +Inf bucket for interpolation
		}
		buckets = append(buckets, sortableBucket{upperBound: ub, cumulativeCount: count})
	}

	// Simple insertion sort — bucket count is small (typically <20).
	for i := 1; i < len(buckets); i++ {
		for j := i; j > 0 && buckets[j].upperBound < buckets[j-1].upperBound; j-- {
			buckets[j], buckets[j-1] = buckets[j-1], buckets[j]
		}
	}
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
// The pattern is matched as a simple prefix-based glob (e.g. "5.." matches "500", "502").
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

// matchPattern performs a simple pattern match where '.' matches any single character.
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

// HealthCheck returns a health.DependencyCheck that reports degraded status
// when any SLO is breached. This integrates with the health package's
// readiness endpoint.
func (c *Checker) HealthCheck() HealthCheckResult {
	statuses := c.Evaluate()
	breached := false
	for _, s := range statuses {
		if s.Breached {
			breached = true
			break
		}
	}
	return HealthCheckResult{
		Name:     "slo",
		Breached: breached,
	}
}

// HealthCheckResult holds the outcome of an SLO health check evaluation.
// Callers can use Breached to decide the health status string.
type HealthCheckResult struct {
	Name     string
	Breached bool
}

// Status returns [health.StatusDegraded] if any SLO is breached, otherwise
// [health.StatusHealthy]. These are string constants matching the health package.
func (r HealthCheckResult) Status() string {
	if r.Breached {
		return "degraded"
	}
	return "healthy"
}

// DependencyCheckFunc returns a function compatible with health.DependencyCheck.Check.
// The returned function evaluates all SLOs and returns "degraded" on breach.
func (c *Checker) DependencyCheckFunc() func() string {
	return func() string {
		return c.HealthCheck().Status()
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
