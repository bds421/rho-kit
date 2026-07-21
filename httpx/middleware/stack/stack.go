package stack

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	mwauditlog "github.com/bds421/rho-kit/httpx/v2/middleware/auditlog"
	"github.com/bds421/rho-kit/httpx/v2/middleware/compress"
	mwcorrelationid "github.com/bds421/rho-kit/httpx/v2/middleware/correlationid"
	mwlogging "github.com/bds421/rho-kit/httpx/v2/middleware/logging"
	mwmetrics "github.com/bds421/rho-kit/httpx/v2/middleware/metrics"
	mwrecover "github.com/bds421/rho-kit/httpx/v2/middleware/recover"
	mwrequestid "github.com/bds421/rho-kit/httpx/v2/middleware/requestid"
	"github.com/bds421/rho-kit/httpx/v2/middleware/secheaders"
	mwtimeout "github.com/bds421/rho-kit/httpx/v2/middleware/timeout"
	mwtracing "github.com/bds421/rho-kit/httpx/v2/middleware/tracing"
	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

// Config controls the default middleware stack.
//
// The 10 Enable* booleans mirror the 10 stages of the middleware chain
// in execution order (outermost first). Each toggle has a corresponding
// `Without*` Option (e.g. WithoutMetrics, WithoutTracing) — that's the
// public API. Callers don't construct Config directly; they call
// [Default] with options. The struct is exported only because the
// Options need a target to mutate.
//
// Flat-bool shape vs. nested-struct (RecoveryConfig + SecurityConfig +
// LoggingConfig + …) is a deliberate v2.0 design choice: the chain
// itself is flat, so a flat config mirrors it 1-to-1 and the Without*
// options stay one-liners. A nested-struct refactor is a v2.x
// candidate that buys nothing the current shape doesn't already give
// the typical caller.
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
	// SecHeadersOptions forwards arbitrary options to [secheaders.New]
	// (audit FR-018). Use this to wire trusted-proxy CIDRs, force
	// HSTS, or any other secheaders option that the stack does not
	// surface as a typed field.
	SecHeadersOptions []secheaders.Option
	Outer             []func(http.Handler) http.Handler
	Inner             []func(http.Handler) http.Handler
	// AuditLogger holds the audit-log sink wired through [WithAuditLog].
	// Default is nil: the audit-log middleware is intentionally omitted from
	// the canonical chain because the sink is service-specific (chain key,
	// cursor key, store). Services that need a tamper-evident audit trail
	// pass [WithAuditLog] explicitly. See docs/audit/THREAT_MODEL.md §4.1.
	AuditLogger *auditlog.Logger
	// AuditLogOptions forwards arbitrary options to the audit-log middleware.
	AuditLogOptions []mwauditlog.Option
	// EnableCompress turns on the response-compression middleware
	// ([middleware/compress]). Off by default — compression surprises
	// clients that expect identity bytes (Range requests, length-prefixed
	// protocols), so it is opt-in via [WithCompress]. When enabled the
	// middleware sits between Inner+AuditLog and the request logger so
	// the handler always emits uncompressed bytes; everything outside
	// the compress layer (request logger, timeout, access log, tracing
	// span) sees the compressed wire bytes.
	EnableCompress  bool
	CompressOptions []compress.Option
}

// Option mutates the Config.
type Option func(*Config)

// Default builds the recommended middleware chain:
// recover -> outer -> security headers -> metrics -> request ID -> correlation ID -> tracing -> logging -> timeout -> request logger -> inner -> auditlog -> handler.
// Additional outer middleware wraps the standard observability/security chain
// but remains inside recover so panics in custom boundary middleware get the
// same JSON 500 response, logging, and panic metrics as handler panics.
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
//
// # Audit logging is NOT wired by default
//
// The tamper-evident audit-log middleware ([middleware/auditlog.Middleware])
// is intentionally omitted from the canonical chain because the underlying
// [auditlog.Logger] requires service-specific chain / cursor keys and a
// concrete store — there is no sensible default. Services that need a
// SOC2-class audit trail pass [WithAuditLog] explicitly; the middleware is
// then inserted at the canonical position (innermost — after inner-wedge
// auth runs and immediately before the handler) so each event captures
// the authenticated actor and the final response status. See
// docs/audit/THREAT_MODEL.md §4.1.
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
		if opt == nil {
			panic("stack: Default option must not be nil")
		}
		opt(&cfg)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	h := handler

	// CaptureRoute must sit immediately around the mux so r.Pattern is
	// recorded into the shared context slot after ServeMux sets it.
	// Without this, every WithContext clone between metrics/tracing
	// (outer) and the mux leaves route="unmatched" and span names bare.
	if cfg.EnableMetrics || cfg.EnableTracing {
		h = mwmetrics.CaptureRoute(h)
	}

	// Audit-log middleware is the innermost stack-managed wrapper: applied
	// before Inner so the Inner wedge (typically auth) runs OUTSIDE it, and
	// the audit entry captures the authenticated actor + response status
	// the handler emitted. Services that prefer audit to sit elsewhere
	// (e.g. before authz) can omit WithAuditLog and add the middleware via
	// WithInner at the position they want.
	if cfg.AuditLogger != nil {
		h = mwauditlog.Middleware(cfg.AuditLogger, cfg.AuditLogOptions...)(h)
	}

	for i := len(cfg.Inner) - 1; i >= 0; i-- {
		h = cfg.Inner[i](h)
	}

	// Compression wraps handler + Inner + AuditLog so the handler always
	// emits uncompressed bytes (auditlog records the response status, not
	// the compressed body bytes; that stays accurate). Everything outside
	// — request logger, timeout, access log, tracing — sees the
	// compressed wire bytes, matching what the client receives.
	if cfg.EnableCompress {
		h = compress.Middleware(cfg.CompressOptions...)(h)
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
		shOpts := make([]secheaders.Option, 0, 1+len(cfg.SecHeadersOptions))
		if cfg.FrameOption != "" {
			shOpts = append(shOpts, secheaders.WithFrameOption(cfg.FrameOption))
		}
		// FR-018 [MED]: forward caller-supplied secheaders options
		// (trusted-proxy CIDRs, force HSTS, etc.) AFTER the typed
		// FrameOption so callers can override defaults.
		shOpts = append(shOpts, cfg.SecHeadersOptions...)
		h = secheaders.New(shOpts...)(h)
	}

	for i := len(cfg.Outer) - 1; i >= 0; i-- {
		h = cfg.Outer[i](h)
	}

	if cfg.EnableRecover {
		recOpts := []mwrecover.Option{mwrecover.WithLogger(cfg.Logger)}
		if cfg.RecoverMetrics != nil {
			recOpts = append(recOpts, mwrecover.WithMetrics(cfg.RecoverMetrics))
		}
		h = mwrecover.Middleware(recOpts...)(h)
	}

	return h
}

// WithQuietPaths sets paths logged at debug level.
func WithQuietPaths(paths ...string) Option {
	copied := append([]string(nil), paths...)
	return func(cfg *Config) { cfg.QuietPaths = append([]string(nil), copied...) }
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
// The stack-level timeout does not auto-bypass WebSocket upgrades or any
// streaming endpoint. The timeout middleware buffers the response, which
// defeats streaming — mount SSE, WebSocket, or long-poll routes on a sub-stack
// built with [WithoutTimeout] (or apply timeout.WithWebSocketUpgradeBypass on
// a custom timeout middleware) rather than relying on automatic detection.
func WithTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("stack: WithTimeout requires a positive duration (use WithoutTimeout to opt out)")
	}
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

// WithoutRecovery disables the panic-recovery middleware. Strongly discouraged
// in production: without it, a handler panic relies on Go's stdlib recovery,
// which logs to ErrorLog (often unset) with no JSON body, no request_id
// correlation, no metric. Use only for tests that intentionally observe
// stdlib's behaviour.
func WithoutRecovery() Option {
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

// WithSecHeadersOptions forwards arbitrary options to [secheaders.New]
// (audit FR-018). Use this to configure trusted-proxy CIDRs for HSTS
// behind TLS-terminating ingress, force HSTS unconditionally, or any
// other secheaders option not surfaced as a typed stack field.
func WithSecHeadersOptions(opts ...secheaders.Option) Option {
	copied := append([]secheaders.Option(nil), opts...)
	return func(cfg *Config) {
		cfg.SecHeadersOptions = append(cfg.SecHeadersOptions, copied...)
	}
}

// WithOuter appends middleware that wraps the full stack.
func WithOuter(mw ...func(http.Handler) http.Handler) Option {
	copied := append([]func(http.Handler) http.Handler(nil), mw...)
	return func(cfg *Config) { cfg.Outer = append(cfg.Outer, copied...) }
}

// WithInner appends middleware applied closest to the handler.
func WithInner(mw ...func(http.Handler) http.Handler) Option {
	copied := append([]func(http.Handler) http.Handler(nil), mw...)
	return func(cfg *Config) { cfg.Inner = append(cfg.Inner, copied...) }
}

// WithCompress enables the response-compression middleware
// ([middleware/compress.Middleware]). Off by default because compression
// surprises some clients (Range requests, length-prefixed protocols), so
// services opt in explicitly. Pass any [compress.Option] values
// (WithGzipLevel, WithMinSize, WithContentTypes, WithEncoder for brotli)
// to tune the chain.
//
// Layout: compression wraps handler + Inner + AuditLog so the handler
// emits uncompressed bytes (auditlog still records the response status
// correctly), while everything outside compression — request logger,
// timeout, access log, tracing span — sees the compressed wire bytes
// that match what the client actually receives.
func WithCompress(opts ...compress.Option) Option {
	copied := append([]compress.Option(nil), opts...)
	return func(cfg *Config) {
		cfg.EnableCompress = true
		cfg.CompressOptions = append(cfg.CompressOptions, copied...)
	}
}

// WithAuditLog wires the tamper-evident audit-log middleware into the chain
// at the canonical innermost position — after the Inner-wedge auth middleware
// runs and immediately before the handler. Each request produces a single
// [auditlog.Event] capturing actor, method, path, response status, and trace
// correlation; panics are recorded as failures so audit / access-log entries
// stay aligned (see [middleware/auditlog.Middleware]).
//
// Audit logging is intentionally NOT enabled by [Default] because the
// underlying [auditlog.Logger] requires service-specific chain / cursor keys
// and a concrete store. Services that need a SOC2-class audit trail must
// pass this option explicitly. Lens F A.10.
//
// Panics on nil to fail fast at wiring time.
func WithAuditLog(logger *auditlog.Logger, opts ...mwauditlog.Option) Option {
	if logger == nil {
		panic("stack: WithAuditLog requires a non-nil *auditlog.Logger")
	}
	copied := append([]mwauditlog.Option(nil), opts...)
	return func(cfg *Config) {
		cfg.AuditLogger = logger
		cfg.AuditLogOptions = append(cfg.AuditLogOptions, copied...)
	}
}
