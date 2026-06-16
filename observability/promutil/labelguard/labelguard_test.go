package labelguard

import (
	"strings"
	"sync"
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

func TestNew_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		New(map[string][]string{"method": {"GET"}}, nil)
	})
}

func TestNew_PanicsOnUnsafeAllowlistDimensions(t *testing.T) {
	cases := []struct {
		name    string
		allowed map[string][]string
	}{
		{name: "empty label", allowed: map[string][]string{"": {"GET"}}},
		{name: "label with space", allowed: map[string][]string{"bad label": {"GET"}}},
		{name: "label with newline", allowed: map[string][]string{"bad\nlabel": {"GET"}}},
		// A Prometheus label NAME must match [a-zA-Z_][a-zA-Z0-9_]*.
		// '.' and ':' are valid in label VALUES but never in NAMES, so
		// an allowlist keyed by such a name could never match a real
		// label and silently disables the guard — reject it loudly.
		{name: "label with dot", allowed: map[string][]string{"bad.label": {"GET"}}},
		{name: "label with colon", allowed: map[string][]string{"bad:label": {"GET"}}},
		{name: "label starting with digit", allowed: map[string][]string{"9label": {"GET"}}},
		{name: "empty value", allowed: map[string][]string{"method": {""}}},
		{name: "value with space", allowed: map[string][]string{"method": {"BAD VALUE"}}},
		{name: "value too long", allowed: map[string][]string{"method": {strings.Repeat("x", 257)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Panics(t, func() {
				New(tc.allowed, WithRegisterer(prometheus.NewRegistry()))
			})
		})
	}
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

func TestPermit_VecNameCacheHitOnRepeatedObservation(t *testing.T) {
	g, reg := freshGuard(t, map[string][]string{
		"method": {"GET"},
	})
	cv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cached_total",
		Help: "test",
	}, []string{"method"})
	require.NoError(t, reg.Register(cv))

	// First rejection resolves and caches the vec name (cache miss);
	// the second rejection must hit the cache and attribute the same
	// vec name, proving the cached value is reused rather than dropped.
	g.ObserveCounter(cv, prometheus.Labels{"method": "DELETE"})
	g.ObserveCounter(cv, prometheus.Labels{"method": "PUT"})

	assert.Equal(t, float64(2),
		testutil.ToFloat64(g.rejected.WithLabelValues("cached_total", "method")))
}

func TestPermit_ConcurrentObservationsAreRaceFree(t *testing.T) {
	g, reg := freshGuard(t, map[string][]string{
		"method": {"GET"},
	})

	const vecs = 8
	const goroutines = 16
	const rounds = 50
	cvs := make([]*prometheus.CounterVec, vecs)
	for i := range cvs {
		cv := prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "race_total_" + string(rune('a'+i)),
			Help: "test",
		}, []string{"method"})
		require.NoError(t, reg.Register(cv))
		cvs[i] = cv
	}

	// Pre-compute the exact number of rejections each vec receives so
	// the assertion can fail if even one rejection is lost or
	// misattributed across goroutines.
	want := make([]int, vecs)
	for round := 0; round < rounds; round++ {
		want[round%vecs] += goroutines
	}

	var wg sync.WaitGroup
	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < rounds; round++ {
				cv := cvs[round%vecs]
				// Mix accepted and rejected to exercise both the
				// cache-miss write path and the lock-free read path.
				g.ObserveCounter(cv, prometheus.Labels{"method": "GET"})
				g.ObserveCounter(cv, prometheus.Labels{"method": "BAD"})
			}
		}()
	}
	wg.Wait()

	// Each vec must have resolved to its own distinct name and recorded
	// every rejection; nothing should be lost or misattributed.
	for i := range cvs {
		name := "race_total_" + string(rune('a'+i))
		assert.Equal(t, float64(want[i]),
			testutil.ToFloat64(g.rejected.WithLabelValues(name, "method")),
			"vec %s should record all its rejections", name)
	}
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
