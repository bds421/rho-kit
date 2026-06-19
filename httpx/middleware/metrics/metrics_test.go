package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetrics_RecordsRequest(t *testing.T) {
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMetrics_RecordsStatusCode(t *testing.T) {
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodPost, "/missing", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestMetrics_UnmatchedPattern(t *testing.T) {
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/no-pattern", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHTTPMetrics_CounterIncrement(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(WithRegisterer(reg))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := m.Middleware(mux)

	for range 3 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}

	var metric dto.Metric
	require.NoError(t, m.requestsTotal.WithLabelValues("GET", "/test", "200").Write(&metric))
	assert.Equal(t, float64(3), metric.GetCounter().GetValue(),
		"counter should reflect 3 requests")
}

func TestHTTPMetrics_DurationObserved(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(WithRegisterer(reg))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /submit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	handler := m.Middleware(mux)

	req := httptest.NewRequest(http.MethodPost, "/submit", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var metric dto.Metric
	observer := m.requestDuration.WithLabelValues("POST", "/submit")
	require.NoError(t, observer.(prometheus.Metric).Write(&metric))
	assert.Equal(t, uint64(1), metric.GetHistogram().GetSampleCount(),
		"histogram should have 1 observation")
}

func TestHTTPMetrics_MethodQualifiedPatternUsesPathLabel(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(WithRegisterer(reg))

	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Pattern = "PATCH /widgets/{id}"
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPatch, "/widgets/42", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)

	var metric dto.Metric
	require.NoError(t, m.requestsTotal.WithLabelValues("PATCH", "/widgets/{id}", "204").Write(&metric))
	assert.Equal(t, float64(1), metric.GetCounter().GetValue())
}

func TestHTTPMetrics_InvalidMethodAndPatternAreBucketed(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(WithRegisterer(reg))

	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Pattern = "bad\npattern"
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodGet, "/bad", nil)
	req.Method = "BREW"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code)

	var metric dto.Metric
	require.NoError(t, m.requestsTotal.WithLabelValues("OTHER", "invalid", "202").Write(&metric))
	assert.Equal(t, float64(1), metric.GetCounter().GetValue())
}

func TestHTTPMetrics_Records500AndRepanicsWhenHandlerPanicsBeforeHeaders(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(WithRegisterer(reg))
	handlerPanic := assert.AnError

	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Pattern = "GET /panic"
		panic(handlerPanic)
	}))

	defer func() {
		got := recover()
		assert.Same(t, handlerPanic, got)

		var metric dto.Metric
		require.NoError(t, m.requestsTotal.WithLabelValues("GET", "/panic", "500").Write(&metric))
		assert.Equal(t, float64(1), metric.GetCounter().GetValue())

		metric.Reset()
		observer := m.requestDuration.WithLabelValues("GET", "/panic")
		require.NoError(t, observer.(prometheus.Metric).Write(&metric))
		assert.Equal(t, uint64(1), metric.GetHistogram().GetSampleCount())
	}()

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))
}

func TestHTTPMetrics_PanicAfterHeaderRecordsWrittenStatus(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(WithRegisterer(reg))
	handlerPanic := assert.AnError

	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Pattern = "GET /created-then-panic"
		w.WriteHeader(http.StatusCreated)
		panic(handlerPanic)
	}))

	defer func() {
		got := recover()
		assert.Same(t, handlerPanic, got)

		var metric dto.Metric
		require.NoError(t, m.requestsTotal.WithLabelValues("GET", "/created-then-panic", "201").Write(&metric))
		assert.Equal(t, float64(1), metric.GetCounter().GetValue())
	}()

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/created-then-panic", nil))
}

func TestNewHTTPMetrics_RepeatedConstructionReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()

	first := NewHTTPMetrics(WithRegisterer(reg))
	// A second construction on the same registry must not panic and must
	// reuse the already-registered collectors so observations from both
	// handles aggregate into one series rather than vanishing into an
	// unregistered local copy.
	second := NewHTTPMetrics(WithRegisterer(reg))

	require.Same(t, first.requestsTotal, second.requestsTotal,
		"second construction should reuse the registered counter")
	require.Same(t, first.requestDuration, second.requestDuration,
		"second construction should reuse the registered histogram")
	require.Same(t, first.requestsInFlight, second.requestsInFlight,
		"second construction should reuse the registered gauge")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /reuse", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handlerFirst := first.Middleware(mux)
	handlerSecond := second.Middleware(mux)
	handlerFirst.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/reuse", nil))
	handlerSecond.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/reuse", nil))

	var metric dto.Metric
	require.NoError(t, second.requestsTotal.WithLabelValues("GET", "/reuse", "200").Write(&metric))
	assert.Equal(t, float64(2), metric.GetCounter().GetValue(),
		"observations from both reused handles should aggregate into one series")
}

func TestNewHTTPMetrics_ConflictingRegistrationPanicsWithErrorText(t *testing.T) {
	reg := prometheus.NewRegistry()

	// Pre-register an http_requests_total collector with a different shape
	// (different help text) so the kit's registration hits a genuine
	// conflict rather than the benign already-registered path. The panic
	// must carry the underlying error text, not a bare opaque message.
	conflicting := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "http",
		Name:      "requests_total",
		Help:      "conflicting collector",
	})
	require.NoError(t, reg.Register(conflicting))

	defer func() {
		got := recover()
		require.NotNil(t, got, "conflicting registration must panic")
		msg, ok := got.(string)
		require.True(t, ok, "panic value should be a string, got %T", got)
		assert.Contains(t, msg, "registration failed",
			"panic should describe the registration failure")
		assert.NotEqual(t, "httpx/metrics: metric registration failed", msg,
			"panic must preserve the underlying error text, not a bare opaque message")
	}()

	NewHTTPMetrics(WithRegisterer(reg))
}

func TestHTTPMetrics_InFlightReturnsToZero(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(WithRegisterer(reg))

	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// During handling, in-flight should be 1.
		var metric dto.Metric
		require.NoError(t, m.requestsInFlight.(prometheus.Metric).Write(&metric))
		assert.Equal(t, float64(1), metric.GetGauge().GetValue())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// After handling, in-flight should be back to 0.
	var metric dto.Metric
	require.NoError(t, m.requestsInFlight.(prometheus.Metric).Write(&metric))
	assert.Equal(t, float64(0), metric.GetGauge().GetValue())
}
