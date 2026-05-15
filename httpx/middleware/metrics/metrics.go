package metrics

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	mw "github.com/bds421/rho-kit/httpx/v2/middleware"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// HTTPMetrics holds Prometheus collectors for HTTP request monitoring.
type HTTPMetrics struct {
	requestsTotal    *prometheus.CounterVec
	requestDuration  *prometheus.HistogramVec
	requestsInFlight prometheus.Gauge
}

// MetricsOption configures [NewHTTPMetrics]. Standardised across the
// kit so every package exposes `NewMetrics(opts ...MetricsOption)`.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for HTTP
// metrics. When unset, [prometheus.DefaultRegisterer] is used.
// Passing nil panics so a miswired "metrics enabled, registerer not
// supplied" caller surfaces at startup rather than going to the
// global default.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("httpx/metrics: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewHTTPMetrics creates and registers HTTP metrics. Pass
// [WithRegisterer] to use a non-default registry. Repeated calls
// reuse already-registered collectors on the same registry.
func NewHTTPMetrics(opts ...MetricsOption) *HTTPMetrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("httpx/metrics: NewHTTPMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	m := &HTTPMetrics{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_requests_total",
				Help: "Total number of HTTP requests.",
			},
			[]string{"method", "route", "status"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "http_request_duration_seconds",
				Help:    "HTTP request duration in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "route"},
		),
		requestsInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Number of HTTP requests currently being processed.",
		}),
	}

	m.requestsTotal = tryRegister(reg, m.requestsTotal).(*prometheus.CounterVec)
	m.requestDuration = tryRegister(reg, m.requestDuration).(*prometheus.HistogramVec)
	m.requestsInFlight = tryRegister(reg, m.requestsInFlight).(prometheus.Gauge)

	return m
}

// Middleware returns an HTTP middleware that records Prometheus metrics.
func (m *HTTPMetrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.requestsInFlight.Inc()
		defer m.requestsInFlight.Dec()

		start := time.Now()
		rec := mw.NewResponseRecorder(w)

		defer func() {
			recovered := recover()

			// Hijacked connections (WebSocket upgrades) bypass HTTP semantics —
			// the recorder's StatusCode is meaningless. Skip metric recording.
			if rec.WasHijacked() {
				if recovered != nil {
					panic(recovered)
				}
				return
			}

			statusCode := rec.Status()
			if recovered != nil && !rec.WroteHeader() {
				statusCode = http.StatusInternalServerError
			}

			duration := time.Since(start).Seconds()
			method := promutil.HTTPMethodLabel(r.Method)
			status := strconv.Itoa(statusCode)

			// Use r.Pattern (Go 1.22+) to get the registered route pattern
			// instead of r.URL.Path which would cause cardinality explosion.
			route := routePatternLabel(r.Pattern)

			m.requestsTotal.WithLabelValues(method, route, status).Inc()
			m.requestDuration.WithLabelValues(method, route).Observe(duration)

			if recovered != nil {
				panic(recovered)
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

func routePatternLabel(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	if method, rest, ok := strings.Cut(pattern, " "); ok && promutil.HTTPMethodLabel(method) == method && rest != "" {
		pattern = rest
	}
	if err := promutil.ValidateStaticLabelValue("route", pattern); err != nil {
		return "invalid"
	}
	return pattern
}

var (
	defaultHTTPMetrics     *HTTPMetrics
	defaultHTTPMetricsOnce sync.Once
)

// Metrics is a convenience wrapper that uses the default Prometheus
// registerer. For custom registerers, use [NewHTTPMetrics] with
// [WithRegisterer].
func Metrics(next http.Handler) http.Handler {
	defaultHTTPMetricsOnce.Do(func() {
		defaultHTTPMetrics = NewHTTPMetrics()
	})
	return defaultHTTPMetrics.Middleware(next)
}

// tryRegister attempts to register a Prometheus collector. If it is already
// registered, the existing collector is returned. This prevents panics when
// the same metrics are created multiple times with the same registerer.
func tryRegister(reg prometheus.Registerer, c prometheus.Collector) prometheus.Collector {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			return are.ExistingCollector
		}
		panic("httpx/metrics: metric registration failed")
	}
	return c
}
