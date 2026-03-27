package slo

import (
	"context"
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
	assert.PanicsWithValue(t, "slo: gatherer must not be nil", func() {
		NewChecker(nil, ErrorRateSLO("a", 0.01, time.Hour))
	})
}

func TestNewChecker_PanicsOnEmptyName(t *testing.T) {
	assert.PanicsWithValue(t, "slo: SLO name must not be empty", func() {
		NewChecker(prometheus.NewRegistry(), SLO{Type: TypeErrorRate, Threshold: 0.01})
	})
}

func TestNewChecker_PanicsOnDuplicateName(t *testing.T) {
	assert.PanicsWithValue(t, `slo: duplicate SLO name "dup"`, func() {
		NewChecker(prometheus.NewRegistry(),
			ErrorRateSLO("dup", 0.01, time.Hour),
			ErrorRateSLO("dup", 0.02, time.Hour),
		)
	})
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
	}, []string{"code"})
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
	}, []string{"code"})
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
	}, []string{"code"})
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
	}, []string{"code"})
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
	}, []string{"code"})
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

func TestChecker_HealthCheck_NoBreach(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"code"})
	reg.MustRegister(total)
	total.WithLabelValues("200").Add(1000)

	c := NewChecker(reg, ErrorRateSLO("err", 0.01, time.Hour))
	result := c.HealthCheck()

	assert.Equal(t, "slo", result.Name)
	assert.False(t, result.Breached)
	assert.Equal(t, "healthy", result.Status())
}

func TestChecker_HealthCheck_Breached(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"code"})
	reg.MustRegister(total)
	total.WithLabelValues("200").Add(900)
	total.WithLabelValues("500").Add(100)

	c := NewChecker(reg, ErrorRateSLO("err", 0.01, time.Hour))
	result := c.HealthCheck()

	assert.True(t, result.Breached)
	assert.Equal(t, "degraded", result.Status())
}

func TestChecker_DependencyCheckFunc(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"code"})
	reg.MustRegister(total)
	total.WithLabelValues("200").Add(1000)

	c := NewChecker(reg, ErrorRateSLO("err", 0.01, time.Hour))
	fn := c.DependencyCheckFunc()

	assert.Equal(t, "healthy", fn(context.Background()))
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
