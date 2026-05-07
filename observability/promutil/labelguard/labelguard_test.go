package labelguard

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freshGuard wires an AllowedLabels onto a private registry so each
// test starts from a zero metric state.
func freshGuard(t *testing.T, allowed map[string][]string) (*AllowedLabels, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	g := New(allowed, WithRegisterer(reg))
	return g, reg
}

func TestObserveCounter_AllowedLabelsPass(t *testing.T) {
	g, reg := freshGuard(t, map[string][]string{
		"method": {"GET", "POST"},
	})
	cv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "requests_total",
		Help: "test",
	}, []string{"method"})
	require.NoError(t, reg.Register(cv))

	g.ObserveCounter(cv, prometheus.Labels{"method": "GET"})
	g.ObserveCounter(cv, prometheus.Labels{"method": "POST"})

	assert.Equal(t, float64(1), testutil.ToFloat64(cv.WithLabelValues("GET")))
	assert.Equal(t, float64(1), testutil.ToFloat64(cv.WithLabelValues("POST")))
}

func TestObserveCounter_DisallowedLabelDropped(t *testing.T) {
	g, reg := freshGuard(t, map[string][]string{
		"method": {"GET", "POST"},
	})
	cv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "requests_total",
		Help: "test",
	}, []string{"method"})
	require.NoError(t, reg.Register(cv))

	g.ObserveCounter(cv, prometheus.Labels{"method": "DELETE"})

	// The vec must NOT have observed the value.
	assert.Equal(t, float64(0), testutil.ToFloat64(cv.WithLabelValues("DELETE")))

	// The rejected counter must have ticked once for (vec=requests_total, label=method).
	assert.Equal(t, float64(1), testutil.ToFloat64(g.rejected.WithLabelValues("requests_total", "method")))
}

func TestObserveHistogram_AllowedLabelsPass(t *testing.T) {
	g, reg := freshGuard(t, map[string][]string{
		"status": {"2xx", "4xx"},
	})
	hv := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "request_duration_seconds",
		Help: "test",
	}, []string{"status"})
	require.NoError(t, reg.Register(hv))

	g.ObserveHistogram(hv, prometheus.Labels{"status": "2xx"}, 0.123)

	count := testutil.CollectAndCount(hv)
	assert.Equal(t, 1, count, "exactly one histogram series should exist")
}

func TestObserveHistogram_DisallowedLabelDropped(t *testing.T) {
	g, reg := freshGuard(t, map[string][]string{
		"status": {"2xx"},
	})
	hv := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "request_duration_seconds",
		Help: "test",
	}, []string{"status"})
	require.NoError(t, reg.Register(hv))

	g.ObserveHistogram(hv, prometheus.Labels{"status": "5xx"}, 1.0)

	count := testutil.CollectAndCount(hv)
	assert.Equal(t, 0, count, "histogram must not have observed the rejected label")

	assert.Equal(t, float64(1),
		testutil.ToFloat64(g.rejected.WithLabelValues("request_duration_seconds", "status")))
}

func TestNew_PanicsOnNilAllowed(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil allowed map")
		}
	}()
	New(nil)
}

func TestObserve_LabelOutsideAllowlistIsUnconstrained(t *testing.T) {
	g, reg := freshGuard(t, map[string][]string{
		"method": {"GET"},
	})
	cv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "demo_total",
		Help: "test",
	}, []string{"method", "tier"})
	require.NoError(t, reg.Register(cv))

	// "tier" is NOT in the allowlist map, so any value passes.
	g.ObserveCounter(cv, prometheus.Labels{"method": "GET", "tier": "anything"})

	assert.Equal(t, float64(1), testutil.ToFloat64(cv.WithLabelValues("GET", "anything")))
	assert.Equal(t, float64(0),
		testutil.ToFloat64(g.rejected.WithLabelValues("demo_total", "method")))
}

func TestObserve_BothLabelsRejectedAreLogged(t *testing.T) {
	g, reg := freshGuard(t, map[string][]string{
		"method": {"GET"},
		"status": {"2xx"},
	})
	cv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "double_total",
		Help: "test",
	}, []string{"method", "status"})
	require.NoError(t, reg.Register(cv))

	g.ObserveCounter(cv, prometheus.Labels{"method": "DELETE", "status": "5xx"})

	assert.Equal(t, float64(1), testutil.ToFloat64(g.rejected.WithLabelValues("double_total", "method")))
	assert.Equal(t, float64(1), testutil.ToFloat64(g.rejected.WithLabelValues("double_total", "status")))
}

func TestObserveCounter_NilVecIsNoOp(t *testing.T) {
	g, _ := freshGuard(t, map[string][]string{})
	// Must not panic.
	g.ObserveCounter(nil, prometheus.Labels{"x": "y"})
	g.ObserveHistogram(nil, prometheus.Labels{"x": "y"}, 1.0)
}

func TestNew_ReusesRejectedCounter(t *testing.T) {
	reg := prometheus.NewRegistry()
	g1 := New(map[string][]string{"a": {"b"}}, WithRegisterer(reg))
	// A second guard on the same registry must reuse the existing
	// rejected counter rather than panic on AlreadyRegisteredError.
	g2 := New(map[string][]string{"c": {"d"}}, WithRegisterer(reg))

	cv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "shared_total",
		Help: "test",
	}, []string{"a"})
	require.NoError(t, reg.Register(cv))

	g1.ObserveCounter(cv, prometheus.Labels{"a": "bad"})
	g2.ObserveCounter(cv, prometheus.Labels{"c": "bad"})

	// Both guards point at the same rejected counter — the registry
	// returned the existing collector during g2's construction.
	assert.Same(t, g1.rejected, g2.rejected)
}
