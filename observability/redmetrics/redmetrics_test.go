package redmetrics

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHTTP_RegistersAllFour(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTP(WithHTTPRegisterer(reg))
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

func TestNewHTTP_ReusesRegisteredCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	first := NewHTTP(WithHTTPRegisterer(reg))
	second := NewHTTP(WithHTTPRegisterer(reg))

	second.Requests.WithLabelValues("/x", "GET", "200").Inc()
	second.Errors.WithLabelValues("/x", "GET", "5xx").Inc()
	second.Duration.WithLabelValues("/x", "GET").Observe(0.1)
	second.InFlight.Inc()

	assert.Same(t, first.Requests, second.Requests)
	assert.Same(t, first.Errors, second.Errors)
	assert.Same(t, first.Duration, second.Duration)
	assert.Same(t, first.InFlight, second.InFlight)
	assert.Equal(t, 1.0, testutil.ToFloat64(first.Requests.WithLabelValues("/x", "GET", "200")))
	assert.Equal(t, 1.0, testutil.ToFloat64(first.Errors.WithLabelValues("/x", "GET", "5xx")))
	assert.Equal(t, 1.0, testutil.ToFloat64(first.InFlight))
}

func TestDefaultBuckets_ReturnDetachedCopies(t *testing.T) {
	httpBuckets := HTTPLatencyBuckets()
	require.NotEmpty(t, httpBuckets)
	httpFirst := httpBuckets[0]
	httpBuckets[0] = 999
	assert.Equal(t, httpFirst, HTTPLatencyBuckets()[0])

	batchBuckets := BatchDurationBuckets()
	require.NotEmpty(t, batchBuckets)
	batchFirst := batchBuckets[0]
	batchBuckets[0] = 999
	assert.Equal(t, batchFirst, BatchDurationBuckets()[0])
}

func TestNewHTTP_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		NewHTTP(WithHTTPRegisterer(prometheus.NewRegistry()), nil)
	})
}

func TestNewHTTP_PanicsOnInvalidMetricNameParts(t *testing.T) {
	assert.Panics(t, func() {
		NewHTTP(WithHTTPRegisterer(prometheus.NewRegistry()), WithHTTPNamespace("my-service"))
	})
	assert.Panics(t, func() {
		NewHTTP(WithHTTPRegisterer(prometheus.NewRegistry()), WithHTTPSubsystem("http api"))
	})
}

func TestHTTPMiddleware_Records2xxAsRequestNotError(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTP(WithHTTPRegisterer(reg))
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
	m := NewHTTP(WithHTTPRegisterer(reg))
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
	m := NewHTTP(WithHTTPRegisterer(reg))
	h := m.Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/anything", nil))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.Requests.WithLabelValues("unknown", "GET", "200")))
}

func TestHTTPMiddleware_InvalidRouteAndMethodAreBucketed(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTP(WithHTTPRegisterer(reg))
	h := m.Middleware(func(*http.Request) string { return "bad\nroute" })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
	)

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Method = "BREW"
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, 1.0, testutil.ToFloat64(m.Requests.WithLabelValues("invalid", "OTHER", "200")))
	observer, err := m.Duration.GetMetricWithLabelValues("invalid", "OTHER")
	require.NoError(t, err)
	metric := &dto.Metric{}
	require.NoError(t, observer.(prometheus.Metric).Write(metric))
	assert.Equal(t, uint64(1), metric.GetHistogram().GetSampleCount())
}

func TestHTTPMiddleware_RouteExtractorPanicRecordsUnknown(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTP(WithHTTPRegisterer(reg))
	h := m.Middleware(func(*http.Request) string {
		panic("route exploded")
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything", nil))

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, 1.0, testutil.ToFloat64(m.Requests.WithLabelValues("unknown", "GET", "202")))
}

func TestHTTPMiddleware_RouteExtractorPanicDoesNotReplaceHandlerPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTP(WithHTTPRegisterer(reg))
	handlerPanic := assert.AnError
	h := m.Middleware(func(*http.Request) string {
		panic("route exploded")
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(handlerPanic)
	}))

	defer func() {
		got := recover()
		assert.Same(t, handlerPanic, got)
		assert.Equal(t, 1.0, testutil.ToFloat64(m.Requests.WithLabelValues("unknown", "GET", "500")))
		assert.Equal(t, 1.0, testutil.ToFloat64(m.Errors.WithLabelValues("unknown", "GET", "5xx")))
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/anything", nil))
}

func TestHTTPMiddleware_InFlightRisesAndFalls(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTP(WithHTTPRegisterer(reg))

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

func TestNewBatch_ReusesRegisteredCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	first := NewBatch(reg, "outbox")
	second := NewBatch(reg, "outbox")

	second.Runs.WithLabelValues("relay", "success").Inc()
	second.Duration.WithLabelValues("relay").Observe(0.1)
	second.InFlight.Inc()

	assert.Same(t, first.Runs, second.Runs)
	assert.Same(t, first.Duration, second.Duration)
	assert.Same(t, first.InFlight, second.InFlight)
	assert.Equal(t, 1.0, testutil.ToFloat64(first.Runs.WithLabelValues("relay", "success")))
	assert.Equal(t, 1.0, testutil.ToFloat64(first.InFlight))
}

func TestNewBatch_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		NewBatch(prometheus.NewRegistry(), "outbox", nil)
	})
}

func TestNewBatch_PanicsOnInvalidMetricNameParts(t *testing.T) {
	assert.Panics(t, func() {
		NewBatch(prometheus.NewRegistry(), "nightly-job")
	})
	assert.Panics(t, func() {
		NewBatch(prometheus.NewRegistry(), "nightly", WithBatchNamespace("ops metrics"))
	})
	assert.Panics(t, func() {
		NewBatch(prometheus.NewRegistry(), "nightly", WithBatchSubsystem("batch-jobs"))
	})
}

func TestNewBatch_BucketsOverride(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewBatch(reg, "test", WithBatchBuckets([]float64{1, 10, 100}))
	m.Duration.WithLabelValues("job").Observe(5)
	count, err := testutil.GatherAndCount(reg, "test_run_duration_seconds")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestWithHTTPBuckets_ClonesInput(t *testing.T) {
	buckets := []float64{1, 10}
	opt := WithHTTPBuckets(buckets)
	buckets[0] = 0.001

	m := NewHTTP(WithHTTPRegisterer(prometheus.NewRegistry()), opt)
	observer, err := m.Duration.GetMetricWithLabelValues("/route", "GET")
	require.NoError(t, err)
	metric := &dto.Metric{}
	require.NoError(t, observer.(prometheus.Metric).Write(metric))
	assert.Equal(t, []float64{1, 10}, histogramBucketBounds(metric))
}

func TestWithHTTPBuckets_OptionReuseClonesOutput(t *testing.T) {
	opt := WithHTTPBuckets([]float64{1, 10})

	var cfg1 httpConfig
	opt(&cfg1)
	var cfg2 httpConfig
	opt(&cfg2)

	cfg1.buckets[0] = 0.001
	assert.Equal(t, []float64{1, 10}, cfg2.buckets)
}

func TestWithBatchBuckets_ClonesInput(t *testing.T) {
	buckets := []float64{1, 10}
	opt := WithBatchBuckets(buckets)
	buckets[0] = 0.001

	m := NewBatch(prometheus.NewRegistry(), "test", opt)
	observer, err := m.Duration.GetMetricWithLabelValues("job")
	require.NoError(t, err)
	metric := &dto.Metric{}
	require.NoError(t, observer.(prometheus.Metric).Write(metric))
	assert.Equal(t, []float64{1, 10}, histogramBucketBounds(metric))
}

func TestWithBatchBuckets_OptionReuseClonesOutput(t *testing.T) {
	opt := WithBatchBuckets([]float64{1, 10})

	var cfg1 batchConfig
	opt(&cfg1)
	var cfg2 batchConfig
	opt(&cfg2)

	cfg1.buckets[0] = 0.001
	assert.Equal(t, []float64{1, 10}, cfg2.buckets)
}

func TestWithHTTPBuckets_PanicsOnInvalid(t *testing.T) {
	cases := map[string]struct {
		buckets   []float64
		want      string
		forbidden []string
	}{
		"empty": {
			buckets: []float64{},
			want:    "redmetrics: buckets must not be empty",
		},
		"non-positive": {
			buckets:   []float64{0, 1, 2},
			want:      "redmetrics: buckets must be positive",
			forbidden: []string{"0"},
		},
		"negative": {
			buckets:   []float64{-1, 1, 2},
			want:      "redmetrics: buckets must be positive",
			forbidden: []string{"-1"},
		},
		"unsorted": {
			buckets:   []float64{1, 0.5, 2},
			want:      "redmetrics: buckets must be strictly increasing",
			forbidden: []string{"0.5"},
		},
		"duplicate": {
			buckets: []float64{1, 1, 2},
			want:    "redmetrics: buckets must be strictly increasing",
		},
	}
	for name, tt := range cases {
		tt := tt
		t.Run(name, func(t *testing.T) {
			defer func() {
				rec := recover()
				require.NotNil(t, rec)
				msg, ok := rec.(string)
				require.True(t, ok, "panic value must be a stable string, got %T", rec)
				assert.Equal(t, tt.want, msg)
				for _, value := range tt.forbidden {
					assert.NotContains(t, msg, value)
				}
			}()
			WithHTTPBuckets(tt.buckets)
		})
	}
}

func TestWithBatchBuckets_PanicsOnInvalid(t *testing.T) {
	assert.PanicsWithValue(t, "redmetrics: buckets must not be empty", func() { WithBatchBuckets(nil) })
	assert.PanicsWithValue(t, "redmetrics: buckets must be strictly increasing", func() { WithBatchBuckets([]float64{0.5, 0.4}) })
	assert.PanicsWithValue(t, "redmetrics: buckets must be positive", func() { WithBatchBuckets([]float64{0, 1}) })
}

func histogramBucketBounds(metric *dto.Metric) []float64 {
	h := metric.GetHistogram()
	out := make([]float64, 0, len(h.GetBucket()))
	for _, bucket := range h.GetBucket() {
		out = append(out, bucket.GetUpperBound())
	}
	return out
}

// flushableRecorder wraps httptest.ResponseRecorder so it satisfies
// [http.Flusher] for the forwarding test.
type flushableRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushableRecorder) Flush() { f.flushed = true }

func TestStatusRecorder_FlushForwarded(t *testing.T) {
	inner := &flushableRecorder{ResponseRecorder: httptest.NewRecorder()}
	rec := newStatusRecorder(inner)
	flusher, ok := http.ResponseWriter(rec).(http.Flusher)
	require.True(t, ok, "statusRecorder should expose http.Flusher")
	flusher.Flush()
	assert.True(t, inner.flushed, "Flush must reach the underlying writer")
}

// hijackableRecorder pretends to be a hijack-capable ResponseWriter so we can
// confirm the recorder forwards the call rather than swallowing it.
type hijackableRecorder struct {
	http.ResponseWriter
	called bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.called = true
	return nil, nil, nil
}

func TestStatusRecorder_HijackForwarded(t *testing.T) {
	inner := &hijackableRecorder{ResponseWriter: httptest.NewRecorder()}
	rec := newStatusRecorder(inner)
	hj, ok := http.ResponseWriter(rec).(http.Hijacker)
	require.True(t, ok)
	_, _, _ = hj.Hijack()
	assert.True(t, inner.called)
}

func TestStatusRecorder_HijackUnsupportedReturnsError(t *testing.T) {
	rec := newStatusRecorder(httptest.NewRecorder())
	hj, ok := http.ResponseWriter(rec).(http.Hijacker)
	require.True(t, ok)
	_, _, err := hj.Hijack()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Hijacker")
}

// pushableRecorder fakes http.Pusher to confirm forwarding.
type pushableRecorder struct {
	http.ResponseWriter
	target string
}

func (p *pushableRecorder) Push(target string, _ *http.PushOptions) error {
	p.target = target
	return nil
}

func TestStatusRecorder_PushForwarded(t *testing.T) {
	inner := &pushableRecorder{ResponseWriter: httptest.NewRecorder()}
	rec := newStatusRecorder(inner)
	pusher, ok := http.ResponseWriter(rec).(http.Pusher)
	require.True(t, ok)
	require.NoError(t, pusher.Push("/asset.js", nil))
	assert.Equal(t, "/asset.js", inner.target)
}

func TestStatusRecorder_PushUnsupportedReturnsErrNotSupported(t *testing.T) {
	rec := newStatusRecorder(httptest.NewRecorder())
	pusher, ok := http.ResponseWriter(rec).(http.Pusher)
	require.True(t, ok)
	err := pusher.Push("/x", nil)
	assert.ErrorIs(t, err, http.ErrNotSupported)
}

func TestStatusRecorder_Unwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := newStatusRecorder(inner)
	type unwrapper interface {
		Unwrap() http.ResponseWriter
	}
	u, ok := http.ResponseWriter(rec).(unwrapper)
	require.True(t, ok)
	assert.Same(t, inner, u.Unwrap())
}

func TestStatusRecorder_ReadFromFallback(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := newStatusRecorder(inner)
	src := strings.NewReader("hello")
	n, err := rec.ReadFrom(src)
	require.NoError(t, err)
	assert.Equal(t, int64(5), n)
	assert.Equal(t, "hello", inner.Body.String())
}
