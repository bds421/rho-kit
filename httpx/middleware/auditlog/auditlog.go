// Package auditlog provides HTTP middleware that automatically captures
// request/response events into the audit log.
package auditlog

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bds421/rho-kit/httpx/middleware/clientip"
	"github.com/bds421/rho-kit/observability/auditlog"
)

// Option configures the audit middleware.
type Option func(*config)

type config struct {
	actorExtractor func(*http.Request) string
	pathFilter     func(string) bool
	statusFilter   func(int) bool
	clientIPFunc   func(*http.Request) string
	trustedProxies []*net.IPNet
}

// WithActorExtractor sets a function that extracts the actor identity from the
// request (e.g., from JWT claims). Default: returns "anonymous".
// A nil fn is ignored so the default actor extractor is preserved.
func WithActorExtractor(fn func(*http.Request) string) Option {
	return func(c *config) {
		if fn != nil {
			c.actorExtractor = fn
		}
	}
}

// WithPathFilter sets a function that decides whether a path should be audited.
// Return true to audit, false to skip. Default: skips /health, /ready, /metrics.
// A nil fn is ignored so the default path filter is preserved.
func WithPathFilter(fn func(string) bool) Option {
	return func(c *config) {
		if fn != nil {
			c.pathFilter = fn
		}
	}
}

// WithStatusFilter sets a function that decides whether a response status should
// be audited. Return true to audit. Default: audits all statuses.
// A nil fn is ignored so the default status filter is preserved.
func WithStatusFilter(fn func(int) bool) Option {
	return func(c *config) {
		if fn != nil {
			c.statusFilter = fn
		}
	}
}

// WithTrustedProxies configures which CIDRs are trusted to set
// X-Forwarded-For when resolving the client IP for audit entries.
// Default: no trusted proxies (only the direct r.RemoteAddr peer is
// recorded), which matches clientip.ClientIP's loopback-only default
// posture. Pass the same list as the access-log middleware so the two
// surfaces agree on what counts as the originating client.
func WithTrustedProxies(nets []*net.IPNet) Option {
	return func(c *config) { c.trustedProxies = nets }
}

// WithClientIPFunc fully overrides the client-IP resolver. Useful for
// platforms whose proxy chain doesn't follow the standard
// X-Forwarded-For shape. The default is
// clientip.ClientIPWithTrustedProxies(r, trustedProxies).
// A nil fn is ignored so the default client-IP resolver is preserved.
func WithClientIPFunc(fn func(*http.Request) string) Option {
	return func(c *config) {
		if fn != nil {
			c.clientIPFunc = fn
		}
	}
}

func defaultPathFilter(path string) bool {
	return !strings.HasPrefix(path, "/health") &&
		!strings.HasPrefix(path, "/ready") &&
		!strings.HasPrefix(path, "/metrics")
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
			if !cfg.pathFilter(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
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
func writeAuditEntry(l *auditlog.Logger, r *http.Request, rec *statusRecorder, cfg config, panicked bool) {
	if !panicked && !cfg.statusFilter(rec.statusCode) {
		return
	}

	status := "success"
	if panicked {
		status = "failure"
	} else if rec.statusCode >= 400 {
		status = "failure"
	}

	auditCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ev := auditlog.Event{
		IPAddress: cfg.clientIPFunc(r),
		Actor:     cfg.actorExtractor(r),
		Action:    r.Method,
		Resource:  r.URL.Path,
		Status:    status,
	}
	if panicked {
		ev.Metadata = panicMetadataJSON
	}
	l.Log(auditCtx, ev)
}

// panicMetadataJSON is the metadata payload attached to entries
// produced when the handler panicked. Encoded once so every panic-path
// entry shares the same bytes.
var panicMetadataJSON = []byte(`{"error":"panic"}`)

type statusRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if sr.wroteHeader {
		return
	}
	sr.statusCode = code
	sr.wroteHeader = true
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wroteHeader {
		sr.WriteHeader(http.StatusOK)
	}
	return sr.ResponseWriter.Write(b)
}

func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}
