package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/httpx"
	idem "github.com/bds421/rho-kit/data/idempotency"
	"github.com/bds421/rho-kit/observability/promutil"
)

// Option configures the idempotency middleware.
type Option func(*config)

// Metrics holds Prometheus counters for idempotency middleware observability.
type Metrics struct {
	hits      prometheus.Counter // cached response replayed
	misses    prometheus.Counter // no cached response, processing
	conflicts prometheus.Counter // key already being processed (409)
	errors    prometheus.Counter // store errors (500)
}

// NewMetrics creates and registers idempotency metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		hits: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "http",
			Subsystem: "idempotency",
			Name:      "hits_total",
			Help:      "Number of requests served from idempotency cache.",
		}),
		misses: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "http",
			Subsystem: "idempotency",
			Name:      "misses_total",
			Help:      "Number of requests processed (no cached response).",
		}),
		conflicts: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "http",
			Subsystem: "idempotency",
			Name:      "conflicts_total",
			Help:      "Number of requests rejected due to concurrent processing (409).",
		}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "http",
			Subsystem: "idempotency",
			Name:      "errors_total",
			Help:      "Number of store errors (500).",
		}),
	}
	promutil.RegisterCollector(reg, m.hits)
	promutil.RegisterCollector(reg, m.misses)
	promutil.RegisterCollector(reg, m.conflicts)
	promutil.RegisterCollector(reg, m.errors)
	return m
}

type config struct {
	userExtractor   func(*http.Request) string
	ttl             time.Duration
	header          string
	requiredMethods map[string]bool
	logger          *slog.Logger
	metrics         *Metrics
}

// WithTTL sets the cache TTL for stored responses. Default: 24h.
func WithTTL(d time.Duration) Option {
	return func(c *config) { c.ttl = d }
}

// WithHeader sets the header name used as idempotency key. Default: "Idempotency-Key".
func WithHeader(name string) Option {
	return func(c *config) { c.header = name }
}

// WithLogger sets the logger for idempotency store errors. Default: slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithMetrics enables Prometheus metrics for the middleware.
func WithMetrics(m *Metrics) Option {
	return func(c *config) { c.metrics = m }
}

// WithRequiredMethods sets the HTTP methods that require an idempotency key.
// Default: POST, PUT, PATCH.
func WithRequiredMethods(methods ...string) Option {
	return func(c *config) {
		c.requiredMethods = make(map[string]bool, len(methods))
		for _, m := range methods {
			c.requiredMethods[m] = true
		}
	}
}

// defaultConfig returns the default middleware configuration.
func defaultConfig() config {
	return config{
		ttl:    24 * time.Hour,
		header: "Idempotency-Key",
		logger: slog.Default(),
		requiredMethods: map[string]bool{
			http.MethodPost:  true,
			http.MethodPut:   true,
			http.MethodPatch: true,
		},
	}
}

// Middleware deduplicates requests by the Idempotency-Key header.
// Non-required methods (by default GET, HEAD, OPTIONS, DELETE) are passed through.
// Returns 400 if the header is missing on required methods.
// Middleware returns HTTP middleware that enforces idempotent request processing.
//
// WARNING: In multi-tenant systems, you MUST use [WithUserExtractor] to scope
// idempotency keys per user. Without it, different users sharing the same
// idempotency key will receive each other's cached responses.
func Middleware(store idem.Store, opts ...Option) func(http.Handler) http.Handler {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.userExtractor == nil {
		cfg.logger.Warn("idempotency middleware created without WithUserExtractor — keys are not user-scoped, which is unsafe in multi-tenant systems")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.requiredMethods[r.Method] {
				next.ServeHTTP(w, r)
				return
			}

			rawKey := r.Header.Get(cfg.header)
			if rawKey == "" {
				httpx.WriteError(w, http.StatusBadRequest, cfg.header+" header is required")
				return
			}
			if len(rawKey) > 256 {
				httpx.WriteError(w, http.StatusBadRequest, cfg.header+" too long (max 256 bytes)")
				return
			}

			userID := ""
			if cfg.userExtractor != nil {
				userID = cfg.userExtractor(r)
			}
			key := fingerprintKey(r.Method, r.URL.Path, rawKey, userID)

			cached, err := store.Get(r.Context(), key)
			if err != nil {
				if cfg.metrics != nil {
					cfg.metrics.errors.Inc()
				}
				httpx.WriteError(w, http.StatusInternalServerError, "idempotency store error")
				return
			}
			if cached != nil {
				if cfg.metrics != nil {
					cfg.metrics.hits.Inc()
				}
				replay(w, cached)
				return
			}

			locked, lockErr := store.TryLock(r.Context(), key, cfg.ttl)
			if lockErr != nil {
				if cfg.metrics != nil {
					cfg.metrics.errors.Inc()
				}
				httpx.WriteError(w, http.StatusInternalServerError, "idempotency store error")
				return
			}
			if !locked {
				if cfg.metrics != nil {
					cfg.metrics.conflicts.Inc()
				}
				httpx.WriteError(w, http.StatusConflict, "request already in progress")
				return
			}
			if cfg.metrics != nil {
				cfg.metrics.misses.Inc()
			}

			rec := &responseCapture{
				ResponseWriter:  w,
				capturedHeaders: make(http.Header),
				statusCode:      http.StatusOK,
				body:            &bytes.Buffer{},
			}

			panicked := true
			defer func() {
				if panicked {
					if unlockErr := store.Unlock(context.Background(), key); unlockErr != nil {
						cfg.logger.Error("idempotency: failed to unlock after panic",
							"error", unlockErr, "key", rawKey)
					}
				}
			}()

			next.ServeHTTP(rec, r)
			panicked = false

			if rec.bodyOverflow {
				cfg.logger.Warn("idempotency: response too large to cache, skipping",
					"key", rawKey)
				if unlockErr := store.Unlock(context.Background(), key); unlockErr != nil {
					cfg.logger.Error("idempotency: failed to unlock after overflow",
						"error", unlockErr, "key", rawKey)
				}
				return
			}

			headers := make(map[string][]string, len(rec.Header()))
			for k, vals := range rec.Header() {
				cp := make([]string, len(vals))
				copy(cp, vals)
				headers[k] = cp
			}
			resp := idem.CachedResponse{
				StatusCode: rec.statusCode,
				Headers:    headers,
				Body:       append([]byte(nil), rec.body.Bytes()...),
			}
			if setErr := store.Set(context.Background(), key, resp, cfg.ttl); setErr != nil {
				cfg.logger.Error("idempotency: failed to cache response, lock held until TTL expiry",
					"error", setErr, "key", rawKey)
				// Do NOT unlock — keeping the lock prevents duplicate execution
				// during the TTL window. The lock expires naturally.
			} else if unlockErr := store.Unlock(context.Background(), key); unlockErr != nil {
				cfg.logger.Error("idempotency: failed to unlock (key blocked until TTL expiry)",
					"error", unlockErr, "key", rawKey)
			}
		})
	}
}

func fingerprintKey(method, path, rawKey, userID string) string {
	h := sha256.New()
	_, _ = io.WriteString(h, method)
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, path)
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, rawKey)
	if userID != "" {
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, userID)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func replay(w http.ResponseWriter, cached *idem.CachedResponse) {
	for k, vals := range cached.Headers {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(cached.StatusCode)
	_, _ = w.Write(cached.Body)
}

const maxCapturedBodySize = 1 << 20 // 1 MiB

type responseCapture struct {
	http.ResponseWriter
	capturedHeaders http.Header
	statusCode      int
	body            *bytes.Buffer
	wroteHeader     bool
	bodyOverflow    bool
}

func (rc *responseCapture) Header() http.Header {
	return rc.capturedHeaders
}

func (rc *responseCapture) WriteHeader(code int) {
	if rc.wroteHeader {
		return
	}
	rc.statusCode = code
	rc.wroteHeader = true
	for k, vals := range rc.capturedHeaders {
		rc.ResponseWriter.Header()[k] = vals
	}
	rc.ResponseWriter.WriteHeader(code)
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	if !rc.wroteHeader {
		rc.WriteHeader(http.StatusOK)
	}
	if !rc.bodyOverflow {
		if rc.body.Len()+len(b) > maxCapturedBodySize {
			rc.bodyOverflow = true
			rc.body.Reset()
		} else {
			rc.body.Write(b)
		}
	}
	return rc.ResponseWriter.Write(b)
}

func (rc *responseCapture) Unwrap() http.ResponseWriter {
	return rc.ResponseWriter
}

// WithUserExtractor sets a function that extracts the user identity from the
// request (e.g., from JWT claims or auth context). When set, the idempotency
// key is scoped per-user, preventing cross-user cache collisions in
// multi-tenant systems.
func WithUserExtractor(fn func(*http.Request) string) Option {
	return func(c *config) { c.userExtractor = fn }
}
