// Package auditlog provides HTTP middleware that automatically captures
// request/response events into the audit log.
package auditlog

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/bds421/rho-kit/observability/auditlog"
)

// Option configures the audit middleware.
type Option func(*config)

type config struct {
	actorExtractor func(*http.Request) string
	pathFilter     func(string) bool
	statusFilter   func(int) bool
}

// WithActorExtractor sets a function that extracts the actor identity from the
// request (e.g., from JWT claims). Default: returns "anonymous".
func WithActorExtractor(fn func(*http.Request) string) Option {
	return func(c *config) { c.actorExtractor = fn }
}

// WithPathFilter sets a function that decides whether a path should be audited.
// Return true to audit, false to skip. Default: skips /health, /ready, /metrics.
func WithPathFilter(fn func(string) bool) Option {
	return func(c *config) { c.pathFilter = fn }
}

// WithStatusFilter sets a function that decides whether a response status should
// be audited. Return true to audit. Default: audits all statuses.
func WithStatusFilter(fn func(int) bool) Option {
	return func(c *config) { c.statusFilter = fn }
}

func defaultPathFilter(path string) bool {
	return !strings.HasPrefix(path, "/health") &&
		!strings.HasPrefix(path, "/ready") &&
		!strings.HasPrefix(path, "/metrics")
}

// Middleware returns HTTP middleware that automatically audits requests.
func Middleware(l *auditlog.Logger, opts ...Option) func(http.Handler) http.Handler {
	cfg := config{
		actorExtractor: func(_ *http.Request) string { return "anonymous" },
		pathFilter:     defaultPathFilter,
		statusFilter:   func(_ int) bool { return true },
	}
	for _, o := range opts {
		o(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.pathFilter(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(rec, r)

			if !cfg.statusFilter(rec.statusCode) {
				return
			}

			status := "success"
			if rec.statusCode >= 400 {
				status = "failure"
			}

			auditCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			l.Log(auditCtx, auditlog.Event{
				IPAddress: r.RemoteAddr,
				Actor:     cfg.actorExtractor(r),
				Action:    r.Method,
				Resource:  r.URL.Path,
				Status:    status,
			})
		})
	}
}

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
