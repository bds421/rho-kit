package metrics

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	mw "github.com/bds421/rho-kit/httpx/v2/middleware"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// routePatternKey holds a pointer to the matched ServeMux pattern so
// outer middleware (this package) can read it after intermediate
// handlers have cloned the request via WithContext.
type routePatternKey struct{}

// routePatternSlot is shared via context between [CaptureRoute]
// (innermost, writes) and [HTTPMetrics.Middleware] (outer, reads).
type routePatternSlot struct {
	pattern string
}

// CaptureRoute is the innermost middleware that records r.Pattern into
// a context slot after the handler returns. ServeMux sets Pattern on
// the exact *http.Request it receives; every WithContext clone between
// the metrics/tracing middleware and the mux leaves the outer request's
// Pattern empty. Place CaptureRoute immediately around the mux
// (stack.Default does this) so route labels and span names are accurate.
func CaptureRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if slot, ok := r.Context().Value(routePatternKey{}).(*routePatternSlot); ok && slot != nil {
			slot.pattern = r.Pattern
		}
	})
}

// EnsureRoutePatternSlot installs a capture slot on ctx when one is not
// already present. Outer middleware (metrics, tracing) call this before
// invoking next so [CaptureRoute] has somewhere to write.
func EnsureRoutePatternSlot(ctx context.Context) context.Context {
	if _, ok := ctx.Value(routePatternKey{}).(*routePatternSlot); ok {
		return ctx
	}
	return context.WithValue(ctx, routePatternKey{}, &routePatternSlot{})
}

// RoutePatternFromContext returns the ServeMux pattern recorded by
// [CaptureRoute], or "" if none was captured.
func RoutePatternFromContext(ctx context.Context) string {
	if slot, ok := ctx.Value(routePatternKey{}).(*routePatternSlot); ok && slot != nil {
		return slot.pattern
	}
	return ""
}

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

	// Namespace="http" preserves the ecosystem-conventional
	// http_requests_total / http_request_duration_seconds /
	// http_requests_in_flight wire-form names while aligning the
	// Go struct shape with the kit's Namespace+Name convention.
	// No Subsystem because adding one would shift the wire form to
	// http_<sub>_requests_total and break every dashboard that
	// queries the de-facto OpenMetrics names.
	m := &HTTPMetrics{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "http",
				Name:      "requests_total",
				Help:      "Total number of HTTP requests.",
			},
			[]string{"method", "route", "status"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "http",
				Name:      "request_duration_seconds",
				Help:      "HTTP request duration in seconds.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"method", "route"},
		),
		requestsInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "http",
			Name:      "requests_in_flight",
			Help:      "Number of HTTP requests currently being processed.",
		}),
	}

	// MustRegisterOrGet (vs a local tryRegister) is type-safe via
	// generics and preserves the registration error text on conflict —
	// e.g. coexisting with redmetrics on the default registry, which
	// doc.go warns about — instead of panicking with no diagnostic.
	m.requestsTotal = promutil.MustRegisterOrGet(reg, m.requestsTotal)
	m.requestDuration = promutil.MustRegisterOrGet(reg, m.requestDuration)
	m.requestsInFlight = promutil.MustRegisterOrGet(reg, m.requestsInFlight)

	return m
}

// Middleware returns an HTTP middleware that records Prometheus metrics.
//
// Route labels: Prefer the pattern recorded by [CaptureRoute] (via
// context) over r.Pattern on this request. Intermediate middleware that
// calls r.WithContext leaves the outer request's Pattern empty even when
// ServeMux matched a route on an inner clone. stack.Default installs
// CaptureRoute innermost so labels stay accurate under the canonical
// chain.
func (m *HTTPMetrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.requestsInFlight.Inc()
		defer m.requestsInFlight.Dec()

		start := time.Now()
		rec := mw.NewResponseRecorder(w)
		r = r.WithContext(EnsureRoutePatternSlot(r.Context()))
		slot, _ := r.Context().Value(routePatternKey{}).(*routePatternSlot)

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

			// Prefer the innermost CaptureRoute pattern; fall back to this
			// request's Pattern for stacks that wrap the mux without
			// intermediate WithContext clones.
			pattern := slot.pattern
			if pattern == "" {
				pattern = r.Pattern
			}
			route := routePatternLabel(pattern)

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
