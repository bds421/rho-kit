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

// NewHTTPMetrics creates and registers HTTP metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewHTTPMetrics(reg prometheus.Registerer) *HTTPMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &HTTPMetrics{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_requests_total",
				Help: "Total number of HTTP requests.",
			},
			[]string{"method", "path", "status"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "http_request_duration_seconds",
				Help:    "HTTP request duration in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "path"},
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
			path := routePatternLabel(r.Pattern)

			m.requestsTotal.WithLabelValues(method, path, status).Inc()
			m.requestDuration.WithLabelValues(method, path).Observe(duration)

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
	if err := promutil.ValidateStaticLabelValue("path", pattern); err != nil {
		return "invalid"
	}
	return pattern
}

var (
	defaultHTTPMetrics     *HTTPMetrics
	defaultHTTPMetricsOnce sync.Once
)

// Metrics is a convenience wrapper that uses the default Prometheus registerer.
// For custom registerers, use NewHTTPMetrics.
func Metrics(next http.Handler) http.Handler {
	defaultHTTPMetricsOnce.Do(func() {
		defaultHTTPMetrics = NewHTTPMetrics(nil)
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
