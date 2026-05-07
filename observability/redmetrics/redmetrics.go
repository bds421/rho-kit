// Package redmetrics provides opinionated RED (Rate, Errors, Duration)
// Prometheus metric sets for HTTP servers and batch jobs.
//
// Compared to writing per-service Counter/Histogram pairs by hand, this
// package gives:
//
//   - Sensible default histogram buckets calibrated for typical
//     request and batch latencies (the existing kit middleware uses
//     buckets that cluster under 1s and miss anything past 10s — a
//     common cause of "p99 looks fine but I see slow tail" bugs).
//   - A consistent label set across services, which keeps Grafana
//     dashboards portable.
//   - Auto-registration and capability-detection of buckets so callers
//     can swap a registerer in tests without rewiring constructors.
//
// gRPC RED metrics are intentionally not in this package to avoid
// pulling the grpc dependency into observability/. Use the same
// pattern in a thin gRPC interceptor when needed.
package redmetrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/promutil"
)

// HTTPLatencyBuckets is the default Histogram bucket set for HTTP
// requests: 5ms → 30s spread on a roughly geometric scale, fine
// resolution under 1s where most healthy traffic lives, coarser past 5s
// where tail-latency is the only signal that matters.
//
// Override via [WithHTTPBuckets] for endpoints with very different
// SLOs (e.g. file uploads).
var HTTPLatencyBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5,
	1, 2.5, 5, 10, 30,
}

// BatchDurationBuckets is the default Histogram bucket set for batch /
// cron jobs: 0.1s → 1h spread that captures both fast jobs (queue
// drains) and slow jobs (nightly reports). The first kit version used
// HTTP-shaped buckets which made every batch run land in the >10s
// bucket — useless for tracking regressions.
var BatchDurationBuckets = []float64{
	0.1, 1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600,
}

// HTTPMetrics holds the four collectors of an HTTP RED set.
type HTTPMetrics struct {
	Requests *prometheus.CounterVec
	Errors   *prometheus.CounterVec
	Duration *prometheus.HistogramVec
	InFlight prometheus.Gauge
}

// HTTPOption configures the [NewHTTP] constructor.
type HTTPOption func(*httpConfig)

type httpConfig struct {
	namespace string
	subsystem string
	buckets   []float64
}

// WithHTTPNamespace sets the Prometheus metric namespace prefix.
// Default: "" (no prefix).
func WithHTTPNamespace(ns string) HTTPOption {
	return func(c *httpConfig) { c.namespace = ns }
}

// WithHTTPSubsystem sets the Prometheus metric subsystem prefix.
// Default: "http".
func WithHTTPSubsystem(s string) HTTPOption {
	return func(c *httpConfig) { c.subsystem = s }
}

// WithHTTPBuckets overrides the default duration histogram buckets.
func WithHTTPBuckets(buckets []float64) HTTPOption {
	return func(c *httpConfig) { c.buckets = buckets }
}

// NewHTTP constructs the standard HTTP RED metric set and registers it
// on reg. Pass [prometheus.NewRegistry] in tests to keep state isolated.
func NewHTTP(reg prometheus.Registerer, opts ...HTTPOption) *HTTPMetrics {
	cfg := httpConfig{
		subsystem: "http",
		buckets:   HTTPLatencyBuckets,
	}
	for _, o := range opts {
		o(&cfg)
	}

	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: cfg.namespace,
		Subsystem: cfg.subsystem,
		Name:      "requests_total",
		Help:      "Total HTTP requests by route, method, and status.",
	}, []string{"route", "method", "status"})

	errs := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: cfg.namespace,
		Subsystem: cfg.subsystem,
		Name:      "errors_total",
		Help:      "Total HTTP responses with status >= 400, by route/method/status_class.",
	}, []string{"route", "method", "status_class"})

	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: cfg.namespace,
		Subsystem: cfg.subsystem,
		Name:      "request_duration_seconds",
		Help:      "HTTP request duration distribution.",
		Buckets:   cfg.buckets,
	}, []string{"route", "method"})

	inflight := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: cfg.namespace,
		Subsystem: cfg.subsystem,
		Name:      "requests_in_flight",
		Help:      "Number of HTTP requests currently being served.",
	})

	if reg != nil {
		promutil.RegisterCollector(reg, requests)
		promutil.RegisterCollector(reg, errs)
		promutil.RegisterCollector(reg, duration)
		promutil.RegisterCollector(reg, inflight)
	}

	return &HTTPMetrics{
		Requests: requests,
		Errors:   errs,
		Duration: duration,
		InFlight: inflight,
	}
}

// Middleware records the four RED metrics around next.
//
// routeFor extracts the route label from the request — typically the
// pattern from the router (chi.RouteContext, gorilla mux Route) rather
// than r.URL.Path which would explode cardinality. If routeFor is nil
// or returns "", the label is "unknown".
func (m *HTTPMetrics) Middleware(routeFor func(*http.Request) string) func(http.Handler) http.Handler {
	if routeFor == nil {
		routeFor = func(*http.Request) string { return "" }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.InFlight.Inc()
			defer m.InFlight.Dec()

			rec := newStatusRecorder(w)
			start := time.Now()
			next.ServeHTTP(rec, r)
			elapsed := time.Since(start)

			route := routeFor(r)
			if route == "" {
				route = "unknown"
			}
			method := r.Method
			status := rec.status

			m.Requests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
			m.Duration.WithLabelValues(route, method).Observe(elapsed.Seconds())
			if status >= 400 {
				m.Errors.WithLabelValues(route, method, statusClass(status)).Inc()
			}
		})
	}
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

// statusRecorder captures the response status for the middleware. Mirror
// of httpx/middleware/internal/response_recorder, vendored here so this
// package has no upward dependency on httpx.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

// BatchMetrics holds the standard collectors for batch / cron jobs.
type BatchMetrics struct {
	Runs     *prometheus.CounterVec   // labels: name, status
	Duration *prometheus.HistogramVec // labels: name
	InFlight prometheus.Gauge
}

// BatchOption configures [NewBatch].
type BatchOption func(*batchConfig)

type batchConfig struct {
	namespace string
	subsystem string
	buckets   []float64
}

// WithBatchNamespace sets the Prometheus metric namespace.
func WithBatchNamespace(ns string) BatchOption {
	return func(c *batchConfig) { c.namespace = ns }
}

// WithBatchSubsystem sets the Prometheus metric subsystem.
// Default: the name passed to [NewBatch] (e.g. "cron", "outbox").
func WithBatchSubsystem(s string) BatchOption {
	return func(c *batchConfig) { c.subsystem = s }
}

// WithBatchBuckets overrides the default duration buckets.
func WithBatchBuckets(buckets []float64) BatchOption {
	return func(c *batchConfig) { c.buckets = buckets }
}

// NewBatch constructs RED metrics for batch / cron workloads. The name
// becomes the default subsystem, so a service that runs both an outbox
// relay and a nightly cron sees `outbox_runs_total` and
// `cron_runs_total` cleanly separated.
func NewBatch(reg prometheus.Registerer, name string, opts ...BatchOption) *BatchMetrics {
	cfg := batchConfig{
		subsystem: name,
		buckets:   BatchDurationBuckets,
	}
	for _, o := range opts {
		o(&cfg)
	}

	runs := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: cfg.namespace,
		Subsystem: cfg.subsystem,
		Name:      "runs_total",
		Help:      "Total batch runs by name and status.",
	}, []string{"name", "status"})

	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: cfg.namespace,
		Subsystem: cfg.subsystem,
		Name:      "run_duration_seconds",
		Help:      "Batch run duration distribution.",
		Buckets:   cfg.buckets,
	}, []string{"name"})

	inflight := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: cfg.namespace,
		Subsystem: cfg.subsystem,
		Name:      "runs_in_flight",
		Help:      "Number of batch runs currently executing.",
	})

	if reg != nil {
		promutil.RegisterCollector(reg, runs)
		promutil.RegisterCollector(reg, duration)
		promutil.RegisterCollector(reg, inflight)
	}

	return &BatchMetrics{Runs: runs, Duration: duration, InFlight: inflight}
}
