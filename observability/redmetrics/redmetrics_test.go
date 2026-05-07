package redmetrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHTTP_RegistersAllFour(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTP(reg)
	require.NotNil(t, m.Requests)
	require.NotNil(t, m.Errors)
	require.NotNil(t, m.Duration)
	require.NotNil(t, m.InFlight)

	// CounterVec / HistogramVec only emit a family in Gather() once at
	// least one labelled series exists. Touch one of each so the
	// existence check is meaningful.
	m.Requests.WithLabelValues("/x", "GET", "200").Add(0)
	m.Errors.WithLabelValues("/x", "GET", "5xx").Add(0)
	m.Duration.WithLabelValues("/x", "GET").Observe(0)

	families, err := reg.Gather()
	require.NoError(t, err)
	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}
	assert.True(t, names["http_requests_total"])
	assert.True(t, names["http_errors_total"])
	assert.True(t, names["http_request_duration_seconds"])
	assert.True(t, names["http_requests_in_flight"])
}

func TestHTTPMiddleware_Records2xxAsRequestNotError(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTP(reg)
	h := m.Middleware(func(*http.Request) string { return "/healthz" })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, 1.0, testutil.ToFloat64(m.Requests.WithLabelValues("/healthz", "GET", "200")))
	// No 4xx/5xx → Errors is unchanged.
	count, err := testutil.GatherAndCount(reg, "http_errors_total")
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestHTTPMiddleware_Records5xxAsError(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTP(reg)
	h := m.Middleware(func(*http.Request) string { return "/api/widgets" })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}),
	)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/widgets", nil))

	assert.Equal(t, 1.0, testutil.ToFloat64(m.Requests.WithLabelValues("/api/widgets", "POST", "500")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.Errors.WithLabelValues("/api/widgets", "POST", "5xx")))
}

func TestHTTPMiddleware_NilRouteExtractorYieldsUnknown(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTP(reg)
	h := m.Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/anything", nil))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.Requests.WithLabelValues("unknown", "GET", "200")))
}

func TestHTTPMiddleware_InFlightRisesAndFalls(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTP(reg)

	gateInside := make(chan struct{})
	gateOutside := make(chan struct{})

	h := m.Middleware(func(*http.Request) string { return "/slow" })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gateInside <- struct{}{}
			<-gateOutside
		}),
	)

	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))
	<-gateInside

	assert.Equal(t, 1.0, testutil.ToFloat64(m.InFlight))
	close(gateOutside)
	// After handler returns, in-flight gauge drops.
	assert.Eventually(t, func() bool {
		return testutil.ToFloat64(m.InFlight) == 0.0
	}, time.Second, 5*time.Millisecond)
}

func TestStatusClass(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{100, "1xx"}, {200, "2xx"}, {299, "2xx"}, {302, "3xx"},
		{400, "4xx"}, {422, "4xx"}, {500, "5xx"}, {599, "5xx"},
	}
	for _, tt := range tests {
		assert.Equalf(t, tt.want, statusClass(tt.status), "statusClass(%d)", tt.status)
	}
}

func TestNewBatch_RegistersAndSubsystemDefaultsToName(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewBatch(reg, "outbox")
	require.NotNil(t, m.Runs)

	// Touch each *Vec so a series exists for Gather().
	m.Runs.WithLabelValues("publish", "success").Add(0)
	m.Duration.WithLabelValues("publish").Observe(0)

	families, err := reg.Gather()
	require.NoError(t, err)
	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}
	assert.True(t, names["outbox_runs_total"], "subsystem should default to the name passed to NewBatch")
	assert.True(t, names["outbox_run_duration_seconds"])
}

func TestNewBatch_BucketsOverride(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewBatch(reg, "test", WithBatchBuckets([]float64{1, 10, 100}))
	m.Duration.WithLabelValues("job").Observe(5)
	count, err := testutil.GatherAndCount(reg, "test_run_duration_seconds")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}
