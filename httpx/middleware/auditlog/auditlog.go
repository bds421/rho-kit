// Package auditlog provides HTTP middleware that automatically captures
// request/response events into the audit log.
package auditlog

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware"
	"github.com/bds421/rho-kit/httpx/v2/middleware/clientip"
	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

// Option configures the audit middleware.
type Option func(*config)

type config struct {
	actorExtractor func(*http.Request) string
	pathFilter     func(string) bool
	statusFilter   func(int) bool
	clientIPFunc   func(*http.Request) string
	trustedProxies []*net.IPNet
	errLogger      *slog.Logger
}

// WithActorExtractor sets a function that extracts the actor identity from the
// request (e.g., from JWT claims). Default: returns "anonymous".
func WithActorExtractor(fn func(*http.Request) string) Option {
	if fn == nil {
		panic("auditlog: WithActorExtractor requires a non-nil function")
	}
	return func(c *config) {
		c.actorExtractor = fn
	}
}

// WithPathFilter sets a function that decides whether a path should be audited.
// Return true to audit, false to skip. Default: skips /health, /ready, /metrics.
func WithPathFilter(fn func(string) bool) Option {
	if fn == nil {
		panic("auditlog: WithPathFilter requires a non-nil function")
	}
	return func(c *config) {
		c.pathFilter = fn
	}
}

// WithStatusFilter sets a function that decides whether a response status should
// be audited. Return true to audit. Default: audits all statuses.
func WithStatusFilter(fn func(int) bool) Option {
	if fn == nil {
		panic("auditlog: WithStatusFilter requires a non-nil function")
	}
	return func(c *config) {
		c.statusFilter = fn
	}
}

// WithTrustedProxies configures which CIDRs are trusted to set
// X-Forwarded-For when resolving the client IP for audit entries.
// Default (nil or empty list): clientip.ClientIPWithTrustedProxies
// falls back to the loopback-only default set (127.0.0.0/8, ::1/128),
// so forwarded headers from loopback peers ARE honored. Passing an
// empty slice does NOT disable proxy trust — to fully opt out, supply a
// WithClientIPFunc resolver that ignores forwarded headers. Pass the
// same list as the access-log middleware so the two surfaces agree on
// what counts as the originating client.
func WithTrustedProxies(nets []*net.IPNet) Option {
	copied := cloneIPNets(nets)
	return func(c *config) { c.trustedProxies = cloneIPNets(copied) }
}

// WithClientIPFunc fully overrides the client-IP resolver. Useful for
// platforms whose proxy chain doesn't follow the standard
// X-Forwarded-For shape. The default is
// clientip.ClientIPWithTrustedProxies(r, trustedProxies).
func WithClientIPFunc(fn func(*http.Request) string) Option {
	if fn == nil {
		panic("auditlog: WithClientIPFunc requires a non-nil function")
	}
	return func(c *config) {
		c.clientIPFunc = fn
	}
}

func defaultPathFilter(path string) bool {
	return !isDefaultOpsPath(path, "/health") &&
		!isDefaultOpsPath(path, "/ready") &&
		!isDefaultOpsPath(path, "/metrics")
}

func isDefaultOpsPath(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// Middleware returns HTTP middleware that automatically audits requests.
//
// The audit Log call runs in a deferred block so a panic in the next
// handler still produces an entry — the recover middleware (assumed to
// wrap this) writes the 500 response, and this middleware records that
// the request panicked. Without the defer, an audit log would silently
// omit panicked requests and operators looking at the audit-log table
// would be unable to correlate 500s in the access log with their
// audit-trail entries.
//
// Performance note: the audit emit is synchronous and bounded by a 5s
// timeout. It runs in the request's own goroutine AFTER the response is
// written, so a degraded audit sink stalls that goroutine for up to 5s,
// delaying keep-alive connection reuse and letting goroutines accumulate at
// roughly rps×5s under sustained sink failure. This is a deliberate
// durability tradeoff (the entry is not lost on a fast process exit); if the
// sink can be slow, front this middleware with a bounded async audit logger.
func Middleware(l *auditlog.Logger, opts ...Option) func(http.Handler) http.Handler {
	if l == nil {
		panic("auditlog: Middleware requires a non-nil *auditlog.Logger")
	}
	cfg := config{
		actorExtractor: func(_ *http.Request) string { return "anonymous" },
		pathFilter:     defaultPathFilter,
		statusFilter:   func(_ int) bool { return true },
	}
	for _, o := range opts {
		if o == nil {
			panic("auditlog: Middleware option must not be nil")
		}
		o(&cfg)
	}
	if cfg.clientIPFunc == nil {
		trusted := cfg.trustedProxies
		cfg.clientIPFunc = func(r *http.Request) string {
			return clientip.ClientIPWithTrustedProxies(r, trusted)
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !safePathFilter(cfg.errLogger, cfg.pathFilter, httpx.RequestPath(r)) {
				next.ServeHTTP(w, r)
				return
			}

			rec := middleware.NewResponseRecorder(w)
			panicked := false
			defer func() {
				if rcv := recover(); rcv != nil {
					panicked = true
					// Write the audit entry first, then re-raise so the
					// recover middleware in front of us still produces
					// the 500 response. Without re-raise, this
					// middleware would swallow the panic and the rest
					// of the chain would behave as if nothing had
					// happened.
					writeAuditEntry(l, r, rec, cfg, panicked)
					panic(rcv)
				}
				if !panicked {
					writeAuditEntry(l, r, rec, cfg, false)
				}
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// writeAuditEntry emits the audit log entry. Extracted so both the
// happy path (handler returned cleanly) and the panic path (deferred
// recovery) can share the same code without duplicating the filter and
// extractor logic.
func writeAuditEntry(l *auditlog.Logger, r *http.Request, rec *middleware.ResponseRecorder, cfg config, panicked bool) {
	statusCode := rec.Status()
	if !panicked && !safeStatusFilter(cfg.errLogger, cfg.statusFilter, statusCode) {
		return
	}

	status := "success"
	if panicked {
		status = "failure"
	} else if statusCode >= 400 {
		status = "failure"
	}

	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()

	ev := auditlog.Event{
		IPAddress: safeAuditIPAddress(safeClientIP(cfg.errLogger, cfg.clientIPFunc, r)),
		Actor:     safeAuditActor(safeActor(cfg.errLogger, cfg.actorExtractor, r)),
		Action:    safeAuditToken(r.Method, auditlog.MaxActionBytes, "method"),
		Resource:  safeAuditResource(r),
		Status:    status,
	}
	if panicked {
		ev.Metadata = panicMetadataJSON
	}
	if cfg.errLogger != nil {
		// LogE surfaces audit-sink failures via the configured
		// logger so operators see when the audit pipeline drops a
		// request. Wave 71 closed a hostile-review finding that the
		// fire-and-forget Log() variant silenced failures
		// unconditionally.
		if err := l.LogE(auditCtx, ev); err != nil {
			cfg.errLogger.Error("auditlog middleware: emit failed",
				redact.Error(err),
			)
		}
	} else {
		l.Log(auditCtx, ev)
	}
}

// WithErrorLogger installs a logger that records auditlog.Logger.Log
// failures. Without it, audit-sink failures are silenced. Pass the
// same logger the service uses elsewhere so operators correlate audit
// drops with the broader runtime context.
//
// Panics if logger is nil — omit the option entirely to opt out.
func WithErrorLogger(logger *slog.Logger) Option {
	if logger == nil {
		panic("auditlog: WithErrorLogger requires a non-nil logger")
	}
	return func(c *config) { c.errLogger = logger }
}

// panicMetadataJSON is the metadata payload attached to entries
// produced when the handler panicked. Encoded once so every panic-path
// entry shares the same bytes.
var panicMetadataJSON = []byte(`{"error":"panic"}`)

func cloneIPNets(in []*net.IPNet) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(in))
	for _, n := range in {
		if n == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, &net.IPNet{
			IP:   append(net.IP(nil), n.IP...),
			Mask: append(net.IPMask(nil), n.Mask...),
		})
	}
	return out
}
