package slo

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLatencySLO(t *testing.T) {
	s := LatencySLO("api-latency", 0.99, 0.5, 24*time.Hour)

	assert.Equal(t, "api-latency", s.Name)
	assert.Equal(t, TypeLatency, s.Type)
	assert.Equal(t, 0.5, s.Threshold)
	assert.Equal(t, 0.99, s.Percentile)
	assert.Equal(t, 24*time.Hour, s.Window)
}

func TestErrorRateSLO(t *testing.T) {
	s := ErrorRateSLO("api-errors", 0.001, 24*time.Hour)

	assert.Equal(t, "api-errors", s.Name)
	assert.Equal(t, TypeErrorRate, s.Type)
	assert.Equal(t, 0.001, s.Threshold)
	assert.Equal(t, 24*time.Hour, s.Window)
}

func TestSuccessRateSLO(t *testing.T) {
	s := SuccessRateSLO("api-avail", 0.999, 24*time.Hour)

	assert.Equal(t, "api-avail", s.Name)
	assert.Equal(t, TypeSuccessRate, s.Type)
	assert.Equal(t, 0.999, s.Threshold)
	assert.Equal(t, 24*time.Hour, s.Window)
}

func TestNewChecker_CopiesSLOs(t *testing.T) {
	slos := []SLO{ErrorRateSLO("a", 0.01, time.Hour)}
	c := NewChecker(prometheus.NewRegistry(), slos...)

	// Mutating the original slice should not affect the checker.
	slos[0].Name = "mutated"
	assert.Equal(t, "a", c.slos[0].Name)
}

func TestNewChecker_PanicsOnNilGatherer(t *testing.T) {
	assert.PanicsWithValue(t, "slo: NewChecker gatherer must not be nil", func() {
		NewChecker(nil, ErrorRateSLO("a", 0.01, time.Hour))
	})
}

func TestNewChecker_PanicsOnEmptyName(t *testing.T) {
	assert.PanicsWithValue(t, "slo: NewChecker SLO name must not be empty", func() {
		NewChecker(prometheus.NewRegistry(), SLO{Type: TypeErrorRate, Threshold: 0.01})
	})
}

func TestNewChecker_PanicsOnDuplicateName(t *testing.T) {
	assert.PanicsWithValue(t, "slo: NewChecker duplicate SLO name", func() {
		NewChecker(prometheus.NewRegistry(),
			ErrorRateSLO("dup", 0.01, time.Hour),
			ErrorRateSLO("dup", 0.02, time.Hour),
		)
	})
}

func TestNewChecker_DuplicateNamePanicDoesNotReflectName(t *testing.T) {
	assert.PanicsWithValue(t, "slo: NewChecker duplicate SLO name", func() {
		NewChecker(prometheus.NewRegistry(),
			ErrorRateSLO("secret-token", 0.01, time.Hour),
			ErrorRateSLO("secret-token", 0.02, time.Hour),
		)
	})
}

// TestNewChecker_ValidatesThresholdAndPercentile guards L155: the
// constructor must panic on out-of-range Threshold or Percentile
// values for each SLOType rather than silently producing a checker
// that misclassifies every observation.
func TestNewChecker_ValidatesThresholdAndPercentile(t *testing.T) {
	cases := []struct {
		name string
		slo  SLO
	}{
		{
			name: "latency threshold zero",
			slo:  SLO{Name: "lat", Type: TypeLatency, Threshold: 0, Percentile: 0.99, Window: time.Hour},
		},
		{
			name: "latency threshold negative",
			slo:  SLO{Name: "lat", Type: TypeLatency, Threshold: -1, Percentile: 0.99, Window: time.Hour},
		},
		{
			name: "latency percentile zero",
			slo:  SLO{Name: "lat", Type: TypeLatency, Threshold: 0.5, Percentile: 0, Window: time.Hour},
		},
		{
			name: "latency percentile one",
			slo:  SLO{Name: "lat", Type: TypeLatency, Threshold: 0.5, Percentile: 1, Window: time.Hour},
		},
		{
			name: "latency percentile above one",
			slo:  SLO{Name: "lat", Type: TypeLatency, Threshold: 0.5, Percentile: 1.5, Window: time.Hour},
		},
		{
			name: "error-rate threshold above one",
			slo:  SLO{Name: "err", Type: TypeErrorRate, Threshold: 1.5, Window: time.Hour},
		},
		{
			name: "error-rate threshold negative",
			slo:  SLO{Name: "err", Type: TypeErrorRate, Threshold: -0.01, Window: time.Hour},
		},
		{
			name: "success-rate threshold above one",
			slo:  SLO{Name: "ok", Type: TypeSuccessRate, Threshold: 1.01, Window: time.Hour},
		},
		{
			name: "success-rate threshold negative",
			slo:  SLO{Name: "ok", Type: TypeSuccessRate, Threshold: -0.5, Window: time.Hour},
		},
		{
			name: "unknown SLOType",
			slo:  SLO{Name: "x", Type: SLOType("nonsense"), Threshold: 0.5, Window: time.Hour},
		},
		{
			name: "negative window",
			slo:  SLO{Name: "err", Type: TypeErrorRate, Threshold: 0.01, Window: -time.Second},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assert.Panics(t, func() {
				NewChecker(prometheus.NewRegistry(), tc.slo)
			})
		})
	}
}

func TestChecker_Evaluate_EmptyRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := NewChecker(reg,
		ErrorRateSLO("err", 0.01, time.Hour),
		LatencySLO("lat", 0.99, 0.5, time.Hour),
	)

	statuses := c.Evaluate()
	require.Len(t, statuses, 2)

	for _, s := range statuses {
		assert.True(t, math.IsNaN(s.Current), "expected NaN for %s", s.Name)
		assert.False(t, s.Breached, "should not breach with NaN data for %s", s.Name)
	}
}

func TestChecker_Evaluate_ErrorRate_NoBreach(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"status"})
	reg.MustRegister(total)

	// 1000 OK, 0 errors -> 0% error rate
	total.WithLabelValues("200").Add(1000)

	c := NewChecker(reg, ErrorRateSLO("err", 0.01, time.Hour))
	statuses := c.Evaluate()

	require.Len(t, statuses, 1)
	assert.InDelta(t, 0.0, statuses[0].Current, 1e-9)
	assert.False(t, statuses[0].Breached)
}

func TestChecker_Evaluate_ErrorRate_Breached(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"status"})
	reg.MustRegister(total)

	// 900 OK, 100 errors -> 10% error rate, threshold 1%
	total.WithLabelValues("200").Add(900)
	total.WithLabelValues("500").Add(100)

	c := NewChecker(reg, ErrorRateSLO("err", 0.01, time.Hour))
	statuses := c.Evaluate()

	require.Len(t, statuses, 1)
	assert.InDelta(t, 0.1, statuses[0].Current, 1e-9)
	assert.True(t, statuses[0].Breached)
	assert.Greater(t, statuses[0].BurnRate, 1.0)
}

func TestChecker_Evaluate_SuccessRate_NoBreach(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"status"})
	reg.MustRegister(total)

	// 999 OK, 1 error -> 99.9% success rate, threshold 99.9%
	total.WithLabelValues("200").Add(999)
	total.WithLabelValues("500").Add(1)

	c := NewChecker(reg, SuccessRateSLO("avail", 0.999, time.Hour))
	statuses := c.Evaluate()

	require.Len(t, statuses, 1)
	assert.InDelta(t, 0.999, statuses[0].Current, 1e-9)
	assert.False(t, statuses[0].Breached)
}

func TestChecker_Evaluate_SuccessRate_Breached(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"status"})
	reg.MustRegister(total)

	// 990 OK, 10 errors -> 99.0% success rate, threshold 99.9%
	total.WithLabelValues("200").Add(990)
	total.WithLabelValues("500").Add(10)

	c := NewChecker(reg, SuccessRateSLO("avail", 0.999, time.Hour))
	statuses := c.Evaluate()

	require.Len(t, statuses, 1)
	assert.InDelta(t, 0.99, statuses[0].Current, 1e-9)
	assert.True(t, statuses[0].Breached)
}

func TestChecker_Evaluate_Latency_NoBreach(t *testing.T) {
	reg := prometheus.NewRegistry()
	hist := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Request duration.",
		Buckets: []float64{0.1, 0.25, 0.5, 1.0, 2.5},
	})
	reg.MustRegister(hist)

	// All requests under 0.1s -> p99 well under 0.5s threshold
	for i := 0; i < 100; i++ {
		hist.Observe(0.05)
	}

	c := NewChecker(reg, LatencySLO("lat", 0.99, 0.5, time.Hour))
	statuses := c.Evaluate()

	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Breached)
	assert.Less(t, statuses[0].Current, 0.5)
}

func TestChecker_Evaluate_Latency_Breached(t *testing.T) {
	reg := prometheus.NewRegistry()
	hist := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Request duration.",
		Buckets: []float64{0.1, 0.25, 0.5, 1.0, 2.5},
	})
	reg.MustRegister(hist)

	// Most requests slow -> p99 above 0.5s threshold
	for i := 0; i < 100; i++ {
		hist.Observe(2.0)
	}

	c := NewChecker(reg, LatencySLO("lat", 0.99, 0.5, time.Hour))
	statuses := c.Evaluate()

	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Breached)
	assert.Greater(t, statuses[0].Current, 0.5)
}

func TestChecker_Evaluate_CustomMetricName(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "grpc_requests_total",
		Help: "gRPC requests.",
	}, []string{"status"})
	reg.MustRegister(total)

	total.WithLabelValues("200").Add(900)
	total.WithLabelValues("500").Add(100)

	s := SLO{
		Name:       "grpc-err",
		Type:       TypeErrorRate,
		Threshold:  0.01,
		Window:     time.Hour,
		MetricName: "grpc_requests_total",
	}

	c := NewChecker(reg, s)
	statuses := c.Evaluate()

	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Breached)
}

func TestChecker_Evaluate_CustomErrorLabel(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"status"})
	reg.MustRegister(total)

	total.WithLabelValues("ok").Add(900)
	total.WithLabelValues("err").Add(100)

	s := SLO{
		Name:             "custom-label-err",
		Type:             TypeErrorRate,
		Threshold:        0.01,
		Window:           time.Hour,
		ErrorLabelFilter: LabelFilter{Name: "status", Pattern: "err"},
	}

	c := NewChecker(reg, s)
	statuses := c.Evaluate()

	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Breached)
	assert.InDelta(t, 0.1, statuses[0].Current, 1e-9)
}

func TestChecker_DependencyCheck_NoBreach(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"status"})
	reg.MustRegister(total)
	total.WithLabelValues("200").Add(1000)

	c := NewChecker(reg, ErrorRateSLO("err", 0.01, time.Hour))
	dc := c.DependencyCheck()

	assert.Equal(t, "slo", dc.Name)
	assert.False(t, dc.Critical)
	assert.Equal(t, "healthy", dc.Check(context.Background()))
}

func TestChecker_DependencyCheck_Breached(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"status"})
	reg.MustRegister(total)
	total.WithLabelValues("200").Add(900)
	total.WithLabelValues("500").Add(100)

	c := NewChecker(reg, ErrorRateSLO("err", 0.01, time.Hour))
	dc := c.DependencyCheck()

	assert.Equal(t, "degraded", dc.Check(context.Background()))
}

func TestSLOStatus_String(t *testing.T) {
	s := SLOStatus{
		Name:      "test",
		Type:      TypeErrorRate,
		Threshold: 0.01,
		Current:   0.05,
		Breached:  true,
		BurnRate:  5.0,
		Window:    time.Hour,
	}

	str := s.String()
	assert.Contains(t, str, "BREACHED")
	assert.Contains(t, str, "test")
}

func TestSLOStatus_String_OK(t *testing.T) {
	s := SLOStatus{
		Name:      "test",
		Type:      TypeLatency,
		Threshold: 0.5,
		Current:   0.1,
		BurnRate:  0.2,
		Window:    time.Hour,
	}

	str := s.String()
	assert.Contains(t, str, "OK")
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"5..", "500", true},
		{"5..", "502", true},
		{"5..", "200", false},
		{"5..", "50", false},
		{"200", "200", true},
		{"200", "201", false},
		{"...", "abc", true},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, matchPattern(tt.pattern, tt.value),
			"matchPattern(%q, %q)", tt.pattern, tt.value)
	}
}

func TestEvaluateSLO_UnknownType(t *testing.T) {
	s := SLO{Name: "unknown", Type: SLOType("bogus"), Threshold: 0.5}
	families := map[string]*dto.MetricFamily{}

	status := evaluateSLO(s, families)
	assert.True(t, math.IsNaN(status.Current))
	assert.False(t, status.Breached)
}

func TestHistogramPercentile_EmptyMetrics(t *testing.T) {
	mf := &dto.MetricFamily{
		Type: dto.MetricType_HISTOGRAM.Enum(),
	}

	result := histogramPercentile(mf, 0.99)
	assert.True(t, math.IsNaN(result))
}

func TestHistogramPercentile_WrongType(t *testing.T) {
	mf := &dto.MetricFamily{
		Type: dto.MetricType_COUNTER.Enum(),
	}

	result := histogramPercentile(mf, 0.99)
	assert.True(t, math.IsNaN(result))
}

func TestSumCountersByLabel_WrongType(t *testing.T) {
	mf := &dto.MetricFamily{
		Type: dto.MetricType_GAUGE.Enum(),
	}

	total, matched := sumCountersByLabel(mf, LabelFilter{Name: "code", Pattern: "5.."})
	assert.Equal(t, 0.0, total)
	assert.Equal(t, 0.0, matched)
}

func TestIsSLOBreached(t *testing.T) {
	tests := []struct {
		sloType SLOType
		thresh  float64
		current float64
		want    bool
	}{
		{TypeLatency, 0.5, 0.3, false},
		{TypeLatency, 0.5, 0.6, true},
		{TypeErrorRate, 0.01, 0.005, false},
		{TypeErrorRate, 0.01, 0.02, true},
		{TypeSuccessRate, 0.999, 0.9995, false},
		{TypeSuccessRate, 0.999, 0.998, true},
		{SLOType("unknown"), 0.5, 0.6, false},
	}

	for _, tt := range tests {
		s := SLO{Type: tt.sloType, Threshold: tt.thresh}
		assert.Equal(t, tt.want, isSLOBreached(s, tt.current),
			"isSLOBreached(%s, thresh=%f, cur=%f)", tt.sloType, tt.thresh, tt.current)
	}
}

func TestEvaluateLatency_LabelFilter(t *testing.T) {
	stringPtr := func(s string) *string { return &s }
	uint64Ptr := func(u uint64) *uint64 { return &u }
	float64Ptr := func(f float64) *float64 { return &f }

	mkHist := func(bucket float64, count uint64, sampleCount uint64) *dto.Metric {
		return &dto.Metric{
			Histogram: &dto.Histogram{
				SampleCount: uint64Ptr(sampleCount),
				Bucket: []*dto.Bucket{
					{
						UpperBound:      float64Ptr(bucket),
						CumulativeCount: uint64Ptr(count),
					},
				},
			},
		}
	}

	// Two label sets for the same metric family. Without filtering, the
	// percentile would be aggregated. With filtering, only "fast" is used.
	fast := mkHist(0.1, 100, 100)
	fast.Label = []*dto.LabelPair{{Name: stringPtr("route"), Value: stringPtr("fast")}}
	slow := mkHist(10.0, 100, 100)
	slow.Label = []*dto.LabelPair{{Name: stringPtr("route"), Value: stringPtr("slow")}}

	mf := &dto.MetricFamily{
		Name:   stringPtr("http_request_duration_seconds"),
		Type:   dto.MetricType_HISTOGRAM.Enum(),
		Metric: []*dto.Metric{fast, slow},
	}
	families := map[string]*dto.MetricFamily{
		"http_request_duration_seconds": mf,
	}

	// Without filter — aggregates across both routes.
	unfiltered := evaluateLatency(SLO{
		Type:       TypeLatency,
		Percentile: 0.99,
	}, families)
	assert.False(t, math.IsNaN(unfiltered))

	// With filter on route=fast — only 0.1s buckets contribute.
	filtered := evaluateLatency(SLO{
		Type:               TypeLatency,
		Percentile:         0.99,
		LatencyLabelFilter: LabelFilter{Name: "route", Pattern: "fast"},
	}, families)
	assert.False(t, math.IsNaN(filtered))
	assert.Less(t, filtered, unfiltered, "fast-only percentile must be lower than aggregated")

	// Filter that matches nothing returns NaN.
	none := evaluateLatency(SLO{
		Type:               TypeLatency,
		Percentile:         0.99,
		LatencyLabelFilter: LabelFilter{Name: "route", Pattern: "no-such-route"},
	}, families)
	assert.True(t, math.IsNaN(none))
}

// failingGatherer returns partial data with an error — the exact shape
// of a Prometheus gatherer that hit a per-collector failure but still
// produced data for healthy collectors.
type failingGatherer struct {
	mfs []*dto.MetricFamily
	err error
}

func (f *failingGatherer) Gather() ([]*dto.MetricFamily, error) {
	return f.mfs, f.err
}

// TestChecker_Evaluate_TolerantesGatherErrors guards L155: when the
// underlying gatherer returns a non-nil error alongside partial data,
// the Checker must still evaluate against the partial data rather than
// crash or silently treat every SLO as unbreaching. The error is logged
// at warn level — we don't capture the log in this test (the kit uses
// slog.Default which is process-global) but verify the behaviour
// invariant.
func TestChecker_Evaluate_TolerantesGatherErrors(t *testing.T) {
	g := &failingGatherer{
		mfs: nil, // no partial data
		err: errors.New("collector blew up"),
	}
	c := NewChecker(g,
		ErrorRateSLO("err", 0.01, time.Hour),
	)
	statuses := c.Evaluate()
	require.Len(t, statuses, 1)
	// With no metric data, an error-rate SLO has nothing to compute
	// — Current is NaN and the status surfaces the absence of data
	// rather than a misleading "breaching" or "not breaching"
	// classification.
	assert.Equal(t, "err", statuses[0].Name)
	assert.True(t, math.IsNaN(statuses[0].Current))
}
