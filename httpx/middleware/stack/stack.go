package stack

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/core/contextutil"
	mwcorrelationid "github.com/bds421/rho-kit/httpx/middleware/correlationid"
	mwlogging "github.com/bds421/rho-kit/httpx/middleware/logging"
	mwmetrics "github.com/bds421/rho-kit/httpx/middleware/metrics"
	mwrecover "github.com/bds421/rho-kit/httpx/middleware/recover"
	mwrequestid "github.com/bds421/rho-kit/httpx/middleware/requestid"
	"github.com/bds421/rho-kit/httpx/middleware/secheaders"
	mwtimeout "github.com/bds421/rho-kit/httpx/middleware/timeout"
	mwtracing "github.com/bds421/rho-kit/httpx/middleware/tracing"
)

// Config controls the default middleware stack.
// Boolean fields are ordered to match middleware execution order (outermost first).
type Config struct {
	Logger              *slog.Logger
	QuietPaths          []string
	EnableRecover       bool
	EnableSecHeaders    bool
	EnableMetrics       bool
	EnableRequestID     bool
	EnableCorrelationID bool
	EnableTracing       bool
	EnableLogging       bool
	EnableTimeout       bool
	EnableReqLogger     bool
	Timeout             time.Duration
	FrameOption         secheaders.FrameOption
	RecoverMetrics      *mwrecover.Metrics
	Outer               []func(http.Handler) http.Handler
	Inner               []func(http.Handler) http.Handler
}

// Option mutates the Config.
type Option func(*Config)

// Default builds the recommended middleware chain:
// recover -> security headers -> metrics -> request ID -> correlation ID -> tracing -> logging -> timeout -> request logger -> inner -> handler
// Additional outer middleware wraps the entire chain.
//
// The request logger is injected so that httpx.Logger(ctx, fallback) returns
// a request-scoped logger in handler code. Recover is the OUTERMOST kit
// middleware so that panics in any subsequent middleware (including secheaders
// and metrics) are caught; secheaders is still applied to the recovery
// response because the recovery writer flows back through the wrapped chain
// from the inside out — the panic is caught before secheaders sealed headers.
// Timeout sits inside logging so 503 timeout responses still appear in access
// logs, and outside the request logger so the handler running under the
// deadline still has the scoped logger.
func Default(handler http.Handler, logger *slog.Logger, opts ...Option) http.Handler {
	cfg := Config{
		Logger:              logger,
		QuietPaths:          []string{"/ready"},
		EnableRecover:       true,
		EnableSecHeaders:    true,
		EnableMetrics:       true,
		EnableRequestID:     true,
		EnableCorrelationID: true,
		EnableTracing:       true,
		EnableLogging:       true,
		EnableTimeout:       true,
		EnableReqLogger:     true,
		Timeout:             30 * time.Second,
		FrameOption:         secheaders.Deny,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	h := handler

	for i := len(cfg.Inner) - 1; i >= 0; i-- {
		h = cfg.Inner[i](h)
	}

	// Both the access-log Logger (via extraAttrs below) and the per-handler
	// WithRequestLogger emit request_id and correlation_id by design:
	// the access-log middleware produces structured access log lines, while
	// WithRequestLogger builds the handler-scoped logger returned by
	// httpx.Logger(ctx, fallback). The duplication is intentional.
	var extraAttrs []func(*http.Request) slog.Attr
	if cfg.EnableRequestID {
		extraAttrs = append(extraAttrs, func(r *http.Request) slog.Attr {
			if rid := contextutil.RequestID(r.Context()); rid != "" {
				return slog.String("request_id", rid)
			}
			return slog.Attr{}
		})
	}
	if cfg.EnableCorrelationID {
		extraAttrs = append(extraAttrs, func(r *http.Request) slog.Attr {
			if cid := contextutil.CorrelationID(r.Context()); cid != "" {
				return slog.String("correlation_id", cid)
			}
			return slog.Attr{}
		})
	}

	if cfg.EnableReqLogger {
		h = mwlogging.WithRequestLogger(cfg.Logger)(h)
	}
	if cfg.EnableTimeout && cfg.Timeout > 0 {
		h = mwtimeout.Timeout(cfg.Timeout)(h)
	}
	if cfg.EnableLogging {
		h = mwlogging.Logger(cfg.Logger, cfg.QuietPaths, extraAttrs...)(h)
	}
	if cfg.EnableTracing {
		h = mwtracing.HTTPMiddleware(h)
	}
	if cfg.EnableCorrelationID {
		h = mwcorrelationid.WithCorrelationID(h)
	}
	if cfg.EnableRequestID {
		h = mwrequestid.WithRequestID(h)
	}
	if cfg.EnableMetrics {
		h = mwmetrics.Metrics(h)
	}
	if cfg.EnableSecHeaders {
		var shOpts []secheaders.Option
		if cfg.FrameOption != "" {
			shOpts = append(shOpts, secheaders.WithFrameOption(cfg.FrameOption))
		}
		h = secheaders.New(shOpts...)(h)
	}
	if cfg.EnableRecover {
		recOpts := []mwrecover.Option{mwrecover.WithLogger(cfg.Logger)}
		if cfg.RecoverMetrics != nil {
			recOpts = append(recOpts, mwrecover.WithMetrics(cfg.RecoverMetrics))
		}
		h = mwrecover.Middleware(recOpts...)(h)
	}

	for i := len(cfg.Outer) - 1; i >= 0; i-- {
		h = cfg.Outer[i](h)
	}

	return h
}

// WithQuietPaths sets paths logged at debug level.
func WithQuietPaths(paths ...string) Option {
	return func(cfg *Config) { cfg.QuietPaths = paths }
}

// WithLogger overrides the default logger.
func WithLogger(l *slog.Logger) Option {
	return func(cfg *Config) { cfg.Logger = l }
}

// WithoutMetrics disables metrics middleware.
func WithoutMetrics() Option {
	return func(cfg *Config) { cfg.EnableMetrics = false }
}

// WithoutRequestID disables request ID middleware.
func WithoutRequestID() Option {
	return func(cfg *Config) { cfg.EnableRequestID = false }
}

// WithoutCorrelationID disables correlation ID middleware.
func WithoutCorrelationID() Option {
	return func(cfg *Config) { cfg.EnableCorrelationID = false }
}

// WithoutTracing disables tracing middleware.
func WithoutTracing() Option {
	return func(cfg *Config) { cfg.EnableTracing = false }
}

// WithoutLogging disables logging middleware.
func WithoutLogging() Option {
	return func(cfg *Config) { cfg.EnableLogging = false }
}

// WithTimeout sets the per-request handler timeout. Must be > 0 to take effect.
// Default: 30s. Handlers exceeding the deadline receive a 503 JSON response;
// the handler goroutine is expected to honour ctx.Done().
//
// WebSocket upgrade requests bypass the timeout middleware automatically. SSE
// or other streaming endpoints should be mounted on a sub-stack built with
// [WithoutTimeout] (the timeout buffers the response, which defeats streaming).
func WithTimeout(d time.Duration) Option {
	return func(cfg *Config) { cfg.Timeout = d }
}

// WithoutTimeout disables the per-request timeout middleware. Use only for
// stacks fronting long-lived or streaming responses; the default 30s timeout
// is the recommended production setting for ordinary request/response handlers.
func WithoutTimeout() Option {
	return func(cfg *Config) { cfg.EnableTimeout = false }
}

// WithoutRequestLogger disables the request-scoped logger middleware.
func WithoutRequestLogger() Option {
	return func(cfg *Config) { cfg.EnableReqLogger = false }
}

// WithoutSecHeaders disables security response headers.
func WithoutSecHeaders() Option {
	return func(cfg *Config) { cfg.EnableSecHeaders = false }
}

// WithoutRecover disables the panic-recovery middleware. Strongly discouraged
// in production: without it, a handler panic relies on Go's stdlib recovery,
// which logs to ErrorLog (often unset) with no JSON body, no request_id
// correlation, no metric. Use only for tests that intentionally observe
// stdlib's behaviour.
func WithoutRecover() Option {
	return func(cfg *Config) { cfg.EnableRecover = false }
}

// WithRecoverMetrics enables the http_panics_total counter on the recover
// middleware. Pass the same prometheus.Registerer used elsewhere in the
// service (typically the one registered in the metrics middleware) so the
// counter shows up alongside http_requests_total.
func WithRecoverMetrics(m *mwrecover.Metrics) Option {
	return func(cfg *Config) { cfg.RecoverMetrics = m }
}

// WithFrameOption sets the X-Frame-Options value. Default: DENY.
// Use [secheaders.SameOrigin] for services that need iframe embedding.
func WithFrameOption(opt secheaders.FrameOption) Option {
	return func(cfg *Config) { cfg.FrameOption = opt }
}

// WithOuter appends middleware that wraps the full stack.
func WithOuter(mw ...func(http.Handler) http.Handler) Option {
	return func(cfg *Config) { cfg.Outer = append(cfg.Outer, mw...) }
}

// WithInner appends middleware applied closest to the handler.
func WithInner(mw ...func(http.Handler) http.Handler) Option {
	return func(cfg *Config) { cfg.Inner = append(cfg.Inner, mw...) }
}
