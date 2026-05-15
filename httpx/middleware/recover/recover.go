// Package recover provides panic-recovery middleware for HTTP handlers.
//
// Without recovery, a handler panic relies on Go's stdlib http.Server recover,
// which logs to ErrorLog (often unset) with no JSON body, no request_id
// correlation, no metric. This middleware catches panics, logs structured
// JSON with the request_id, increments a Prometheus counter, and writes a
// 500 response — unless the response was already started, in which case it
// just logs (the connection cannot be cleanly recovered).
//
// Place this middleware as the OUTERMOST layer in the chain so that panics
// in any subsequent middleware are caught. stack.Default does this for you.
//
// asvs: V7.1.1, V14.4.1
package recover

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Option configures the Middleware.
type Option func(*config)

type config struct {
	logger     *slog.Logger
	statusCode int
	body       func(r *http.Request, panicValue any) []byte
	stackTrace bool
	metrics    *Metrics
}

// Metrics holds Prometheus counters for panic observability.
type Metrics struct {
	panics *prometheus.CounterVec
}

// MetricsOption configures the recover-middleware metric constructor.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer. Unset defaults to
// [prometheus.DefaultRegisterer]; passing nil panics.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("middleware/recover: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics registers and returns the panic counter. Pass
// [WithRegisterer] for a non-default registry. Safe to call
// repeatedly; duplicate construction reuses the collector already
// registered on reg.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("middleware/recover: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "http",
		Name:      "panics_total",
		Help:      "Number of HTTP handler panics recovered by the recover middleware.",
	}, []string{"method"})
	c = promutil.MustRegisterOrGet(reg, c)
	return &Metrics{panics: c}
}

// WithLogger overrides the default slog.Default() logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithStatusCode overrides the response status. Default: 500.
func WithStatusCode(code int) Option {
	return func(c *config) { c.statusCode = code }
}

// WithBody overrides the response body builder.
//
// Default: {"error":"internal server error","code":"INTERNAL","request_id":"..."}.
func WithBody(builder func(r *http.Request, panicValue any) []byte) Option {
	return func(c *config) {
		if builder != nil {
			c.body = builder
		}
	}
}

// WithStackTrace toggles stack-trace capture. Default: enabled.
//
// Disabling reduces log volume in environments where panics are otherwise
// noisy (e.g. fuzz / property-based test harnesses), but you almost always
// want this on in production.
func WithStackTrace(enabled bool) Option {
	return func(c *config) { c.stackTrace = enabled }
}

// WithMetrics attaches a panic counter to the middleware.
func WithMetrics(m *Metrics) Option {
	return func(c *config) { c.metrics = m }
}

// Middleware returns a panic-recovery middleware. Defer with care: this MUST
// be the outermost middleware so that a panic in any inner layer (including
// other middleware) is caught.
//
// Behaviour:
//   - http.ErrAbortHandler is treated as a deliberate abort: re-raised to let
//     net/http's handler abort the response. No log, no metric.
//   - Panics that fire after the response has started are logged with a
//     warning that the response cannot be cleanly recovered. Status counter
//     records the panic but no JSON body is written (the bytes have already
//     been flushed and the headers sent).
//   - Otherwise: 500 JSON body with request_id correlation, structured log
//     entry with stack trace, panic counter incremented.
func Middleware(opts ...Option) func(http.Handler) http.Handler {
	cfg := config{
		logger:     slog.Default(),
		statusCode: http.StatusInternalServerError,
		body:       defaultBody,
		stackTrace: true,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("middleware/recover: Middleware: option must not be nil")
		}
		opt(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &recordingWriter{ResponseWriter: w}
			defer func() {
				rv := recover() //nolint:predeclared // intentional — package is also named recover, but the predeclared identifier still resolves here
				if rv == nil {
					return
				}
				if rv == http.ErrAbortHandler {
					// Programmer signalled abort; let net/http's outer
					// recover close the connection without logging.
					panic(rv)
				}
				handlePanic(rec, r, rv, &cfg)
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// handlePanic does the actual recovery work. Split out for readability and
// because the deferred body is the hottest path.
func handlePanic(rec *recordingWriter, r *http.Request, rv any, cfg *config) {
	if cfg.metrics != nil {
		cfg.metrics.panics.WithLabelValues(promutil.HTTPMethodLabel(r.Method)).Inc()
	}

	requestID := contextutil.RequestID(r.Context())

	attrs := []any{
		"method", r.Method,
		redact.String("path", httpx.RequestPath(r)),
		redact.Panic(rv),
	}
	if requestID != "" {
		attrs = append(attrs, "request_id", requestID)
	}
	if cfg.stackTrace {
		attrs = append(attrs, "stack", string(debug.Stack()))
	}

	if rec.wroteHeader {
		// Response already in flight. We cannot cleanly send a 500 — the
		// status line and some bytes have already been written. Log loudly
		// so operators see this as the most operationally severe case
		// (silent corruption of a partially-sent response).
		attrs = append(attrs, "response_started", true, "status_already_sent", rec.statusCode)
		cfg.logger.Error("http: panic after response started — connection corrupted", attrs...)
		return
	}

	cfg.logger.Error("http: handler panic recovered", attrs...)

	rec.Header().Set("Content-Type", "application/json")
	rec.WriteHeader(cfg.statusCode)
	_, _ = rec.Write(safeBody(r, rv, cfg))
}

func safeBody(r *http.Request, rv any, cfg *config) (body []byte) {
	defer func() {
		if rec := recover(); rec != nil {
			logger := cfg.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Error("recover: body builder panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			body = defaultBody(r, rv)
		}
	}()
	return cfg.body(r, rv)
}

// defaultBody emits {"error":"internal server error","code":"INTERNAL","request_id":"..."}.
//
// Avoids encoding/json to keep the recovery path allocation-light.
func defaultBody(r *http.Request, _ any) []byte {
	rid := contextutil.RequestID(r.Context())
	if rid == "" {
		return []byte(`{"error":"internal server error","code":"INTERNAL"}` + "\n")
	}
	// Quote-escape the request_id defensively even though contextutil only
	// produces ASCII-safe IDs — guards against future contract drift.
	return []byte(`{"error":"internal server error","code":"INTERNAL","request_id":` + jsonString(rid) + `}` + "\n")
}

// jsonString returns s wrapped in JSON-quoted form. Hand-rolled to avoid the
// encoding/json dependency in the recovery hot path.
func jsonString(s string) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			out = append(out, '\\', c)
		case c < 0x20:
			out = append(out, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
		default:
			out = append(out, c)
		}
	}
	out = append(out, '"')
	return string(out)
}

// recordingWriter wraps the underlying ResponseWriter to detect whether a
// header has been written. Lighter than the package-level ResponseRecorder
// because we only need wroteHeader; the recover path runs once per panic and
// the wrapper allocates per-request, so we keep it minimal.
type recordingWriter struct {
	http.ResponseWriter
	wroteHeader bool
	statusCode  int
}

func (rw *recordingWriter) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.wroteHeader = true
		rw.statusCode = code
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *recordingWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.wroteHeader = true
		rw.statusCode = http.StatusOK
	}
	return rw.ResponseWriter.Write(b)
}

// Unwrap exposes the underlying writer for http.ResponseController.
func (rw *recordingWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// Hijack delegates if supported; matches the rest of the kit's response
// wrappers. After hijack the wroteHeader flag stays false because hijacked
// connections write their own framing.
//
//nolint:wrapcheck // direct delegation by design
func (rw *recordingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("recover: underlying ResponseWriter does not implement http.Hijacker")
}

// Flush delegates if supported.
func (rw *recordingWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
