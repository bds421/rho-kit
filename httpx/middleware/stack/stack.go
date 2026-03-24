package stack

import (
	"log/slog"
	"net/http"

	"github.com/bds421/rho-kit/httpx"
	mwcorrelationid "github.com/bds421/rho-kit/httpx/middleware/correlationid"
	mwlogging "github.com/bds421/rho-kit/httpx/middleware/logging"
	mwmetrics "github.com/bds421/rho-kit/httpx/middleware/metrics"
	mwrequestid "github.com/bds421/rho-kit/httpx/middleware/requestid"
	"github.com/bds421/rho-kit/httpx/middleware/secheaders"
	mwtracing "github.com/bds421/rho-kit/httpx/middleware/tracing"
)

// Config controls the default middleware stack.
type Config struct {
	Logger              *slog.Logger
	QuietPaths          []string
	EnableMetrics       bool
	EnableRequestID     bool
	EnableTracing       bool
	EnableLogging       bool
	EnableReqLogger     bool
	EnableCorrelationID bool
	EnableSecHeaders    bool
	FrameOption         secheaders.FrameOption
	Outer               []func(http.Handler) http.Handler
	Inner               []func(http.Handler) http.Handler
}

// Option mutates the Config.
type Option func(*Config)

// Default builds the recommended middleware chain:
// security headers -> metrics -> request ID -> correlation ID -> tracing -> request logger -> logging -> inner -> handler
// Additional outer middleware wraps the entire chain.
// The request logger is injected so that httpx.Logger(ctx, fallback) returns
// a request-scoped logger in handler code.
func Default(handler http.Handler, logger *slog.Logger, opts ...Option) http.Handler {
	cfg := Config{
		Logger:           logger,
		QuietPaths:       []string{"/ready"},
		EnableMetrics:       true,
		EnableRequestID:     true,
		EnableCorrelationID: true,
		EnableTracing:       true,
		EnableLogging:       true,
		EnableReqLogger:     true,
		EnableSecHeaders:    true,
		FrameOption:      secheaders.Deny,
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

	var extraAttrs []func(*http.Request) slog.Attr
	if cfg.EnableRequestID {
		extraAttrs = append(extraAttrs, func(r *http.Request) slog.Attr {
			return slog.String("request_id", httpx.RequestID(r.Context()))
		})
	}
	if cfg.EnableCorrelationID {
		extraAttrs = append(extraAttrs, func(r *http.Request) slog.Attr {
			return slog.String("correlation_id", httpx.CorrelationID(r.Context()))
		})
	}

	if cfg.EnableReqLogger {
		h = mwlogging.WithRequestLogger(cfg.Logger)(h)
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

// WithoutRequestLogger disables the request-scoped logger middleware.
func WithoutRequestLogger() Option {
	return func(cfg *Config) { cfg.EnableReqLogger = false }
}

// WithoutSecHeaders disables security response headers.
func WithoutSecHeaders() Option {
	return func(cfg *Config) { cfg.EnableSecHeaders = false }
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
