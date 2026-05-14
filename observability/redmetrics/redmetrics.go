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
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// httpLatencyBuckets is the default Histogram bucket set for HTTP
// requests: 5ms → 30s spread on a roughly geometric scale, fine
// resolution under 1s where most healthy traffic lives, coarser past 5s
// where tail-latency is the only signal that matters.
//
// Override via [WithHTTPBuckets] for endpoints with very different
// SLOs (e.g. file uploads).
var httpLatencyBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5,
	1, 2.5, 5, 10, 30,
}

// HTTPLatencyBuckets returns the default Histogram bucket set for HTTP
// requests. The returned slice is detached and safe to mutate.
func HTTPLatencyBuckets() []float64 {
	return append([]float64(nil), httpLatencyBuckets...)
}

// batchDurationBuckets is the default Histogram bucket set for batch /
// cron jobs: 0.1s → 1h spread that captures both fast jobs (queue
// drains) and slow jobs (nightly reports). The first kit version used
// HTTP-shaped buckets which made every batch run land in the >10s
// bucket — useless for tracking regressions.
var batchDurationBuckets = []float64{
	0.1, 1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600,
}

// BatchDurationBuckets returns the default Histogram bucket set for batch /
// cron jobs. The returned slice is detached and safe to mutate.
func BatchDurationBuckets() []float64 {
	return append([]float64(nil), batchDurationBuckets...)
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
	namespace  string
	subsystem  string
	buckets    []float64
	registerer prometheus.Registerer
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
//
// Buckets must be non-empty, strictly increasing, and all positive.
// Prometheus would otherwise panic at registration with a less actionable
// message; we validate up front so misconfiguration surfaces at boot.
func WithHTTPBuckets(buckets []float64) HTTPOption {
	validateBuckets(buckets)
	buckets = append([]float64(nil), buckets...)
	return func(c *httpConfig) { c.buckets = append([]float64(nil), buckets...) }
}

// HTTPRegisterer is a typed option for pinning the Prometheus
// registerer passed to [NewHTTP]. The function is exported via the
// HTTPOption type itself — see [WithHTTPRegisterer].
type httpRegistererOption struct{ reg prometheus.Registerer }

// WithHTTPRegisterer pins the Prometheus registerer used to register
// the HTTP RED metric set. When unset, the [prometheus.DefaultRegisterer]
// is used. Replaces the v1 positional NewHTTP(reg, ...) signature so
// all kit metric constructors share the same options-only shape.
func WithHTTPRegisterer(reg prometheus.Registerer) HTTPOption {
	if reg == nil {
		panic("redmetrics: WithHTTPRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *httpConfig) { c.registerer = reg }
}

// NewHTTP constructs the standard HTTP RED metric set and registers it
// on the configured registerer. Pass [WithHTTPRegisterer] (typically
// with [prometheus.NewRegistry]) in tests to keep state isolated.
func NewHTTP(opts ...HTTPOption) *HTTPMetrics {
	cfg := httpConfig{
		subsystem:  "http",
		buckets:    HTTPLatencyBuckets(),
		registerer: prometheus.DefaultRegisterer,
	}
	for _, o := range opts {
		if o == nil {
			panic("redmetrics: HTTP option must not be nil")
		}
		o(&cfg)
	}
	validateMetricNamePart("HTTP namespace", cfg.namespace)
	validateMetricNamePart("HTTP subsystem", cfg.subsystem)
	reg := cfg.registerer

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
		requests = promutil.MustRegisterOrGet(reg, requests)
		errs = promutil.MustRegisterOrGet(reg, errs)
		duration = promutil.MustRegisterOrGet(reg, duration)
		inflight = promutil.MustRegisterOrGet(reg, inflight)
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

			// Always record metrics, even on panic. A panicking handler
			// without this recover would skip Requests/Duration/Errors
			// entirely, hiding the worst-quality outcomes from
			// dashboards. We re-panic so upstream recover middleware
			// can still log and respond.
			defer func() {
				rr := recover()
				route := safeRouteLabel(routeFor, r)
				method := promutil.HTTPMethodLabel(r.Method)
				status := rec.status
				if rr != nil {
					// If headers were not yet written, force a 500 for
					// the metric. Don't write to w — leave that to the
					// outer recover middleware.
					if !rec.wrote {
						status = http.StatusInternalServerError
					}
				}

				elapsed := time.Since(start)
				m.Requests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
				m.Duration.WithLabelValues(route, method).Observe(elapsed.Seconds())
				if status >= 400 {
					m.Errors.WithLabelValues(route, method, statusClass(status)).Inc()
				}

				if rr != nil {
					panic(rr)
				}
			}()

			next.ServeHTTP(rec, r)
		})
	}
}

func safeRouteLabel(routeFor func(*http.Request) string, r *http.Request) (route string) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("redmetrics: route extractor panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			route = "unknown"
		}
	}()
	return routeLabel(routeFor(r))
}

func routeLabel(route string) string {
	if route == "" {
		return "unknown"
	}
	if err := promutil.ValidateStaticLabelValue("route", route); err != nil {
		return "invalid"
	}
	return route
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

// Unwrap exposes the underlying ResponseWriter for [http.ResponseController].
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// Flush forwards to the underlying writer when it implements [http.Flusher].
// SSE / chunked-transfer handlers depend on Flush reaching the wire; without
// this delegation the wrapper would silently swallow the call.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer when it implements [http.Hijacker]
// (WebSocket upgrades). The recorder loses meaning after hijack; we leave the
// captured status as-is so middleware metrics still record the pre-hijack
// status code.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("redmetrics: underlying ResponseWriter does not implement http.Hijacker")
}

// Push forwards to the underlying writer when it implements [http.Pusher]
// (HTTP/2 server push). Returns [http.ErrNotSupported] when not supported,
// matching the standard library convention.
func (s *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := s.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// ReadFrom lets handlers using [io.Copy] take the optimised sendfile path
// when the underlying writer is an [io.ReaderFrom]. Without this delegation
// the wrapper would force the generic copy loop, hurting large-file throughput.
func (s *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if !s.wrote {
		s.wrote = true
	}
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(writerOnly{s.ResponseWriter}, src)
}

// writerOnly hides ReadFrom from io.Copy so the fallback in
// [statusRecorder.ReadFrom] uses the generic copy loop and does not re-enter.
type writerOnly struct{ io.Writer }

// BatchMetrics holds the standard collectors for batch / cron jobs.
type BatchMetrics struct {
	Runs     *prometheus.CounterVec   // labels: name, status
	Duration *prometheus.HistogramVec // labels: name
	InFlight prometheus.Gauge
}

// BatchOption configures [NewBatch].
type BatchOption func(*batchConfig)

type batchConfig struct {
	namespace  string
	subsystem  string
	buckets    []float64
	registerer prometheus.Registerer
}

// WithBatchRegisterer pins the Prometheus registerer used to register
// the batch RED metric set. When unset, [prometheus.DefaultRegisterer]
// is used. Replaces the v1 positional NewBatch(reg, ...) signature so
// all kit metric constructors share the same options-only shape.
func WithBatchRegisterer(reg prometheus.Registerer) BatchOption {
	if reg == nil {
		panic("redmetrics: WithBatchRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *batchConfig) { c.registerer = reg }
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
//
// Buckets must be non-empty, strictly increasing, and all positive. See
// [WithHTTPBuckets] for rationale.
func WithBatchBuckets(buckets []float64) BatchOption {
	validateBuckets(buckets)
	buckets = append([]float64(nil), buckets...)
	return func(c *batchConfig) { c.buckets = append([]float64(nil), buckets...) }
}

// validateBuckets enforces the invariants Prometheus assumes for histogram
// buckets — non-empty, strictly increasing, all positive. Panics with a
// clear, attributable message so the caller can fix the option site.
func validateBuckets(buckets []float64) {
	if len(buckets) == 0 {
		panic("redmetrics: buckets must not be empty")
	}
	prev := 0.0
	for i, b := range buckets {
		if b <= 0 {
			panic("redmetrics: buckets must be positive")
		}
		if i > 0 && b <= prev {
			panic("redmetrics: buckets must be strictly increasing")
		}
		prev = b
	}
}

// NewBatch constructs RED metrics for batch / cron workloads. The name
// becomes the default subsystem, so a service that runs both an outbox
// relay and a nightly cron sees `outbox_runs_total` and
// `cron_runs_total` cleanly separated. Pass [WithBatchRegisterer] to
// use a non-default registry.
func NewBatch(name string, opts ...BatchOption) *BatchMetrics {
	cfg := batchConfig{
		subsystem:  name,
		buckets:    BatchDurationBuckets(),
		registerer: prometheus.DefaultRegisterer,
	}
	for _, o := range opts {
		if o == nil {
			panic("redmetrics: Batch option must not be nil")
		}
		o(&cfg)
	}
	validateMetricNamePart("Batch namespace", cfg.namespace)
	validateMetricNamePart("Batch subsystem", cfg.subsystem)
	reg := cfg.registerer

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
		runs = promutil.MustRegisterOrGet(reg, runs)
		duration = promutil.MustRegisterOrGet(reg, duration)
		inflight = promutil.MustRegisterOrGet(reg, inflight)
	}

	return &BatchMetrics{Runs: runs, Duration: duration, InFlight: inflight}
}

func validateMetricNamePart(field, value string) {
	if err := promutil.ValidateMetricNamePart(field, value); err != nil {
		panic("redmetrics: metric name part is invalid")
	}
}
