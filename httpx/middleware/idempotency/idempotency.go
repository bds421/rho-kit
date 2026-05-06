package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	idem "github.com/bds421/rho-kit/data/idempotency"
	"github.com/bds421/rho-kit/httpx"
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

// maxFingerprintBodySize is the largest request body we'll buffer to compute
// a fingerprint. Bodies larger than this are not fingerprinted — the
// middleware degrades to "no body comparison" rather than blocking the
// request, on the assumption that very large payloads are the wrong shape
// for idempotent retry semantics anyway.
const maxFingerprintBodySize = 1 << 20 // 1 MiB

type config struct {
	userExtractor   func(*http.Request) string
	ttl             time.Duration
	header          string
	requiredMethods map[string]bool
	logger          *slog.Logger
	metrics         *Metrics
	fingerprintBody bool
	allowSharedKeys bool
	preserveHeaders map[string]bool // optional override of identityResponseHeaders
}

// identityResponseHeaders are stripped from cached responses before replay.
// Replaying these from one user's response to another user's request is the
// classic idempotency-leak bug: Set-Cookie hands the original user's session
// to whoever replays, Authorization echoes the original credential, etc.
// The stripping is conservative — Cache-Control, ETag, Content-Type, and the
// usual application-shape headers stay because they're response-bound rather
// than caller-bound.
//
// Keys must be in http.CanonicalHeaderKey form. http.Header normalises on
// write (Set / Add) but the iteration over rec.Header() returns whatever
// the handler wrote — so we canonicalise on lookup.
var identityResponseHeaders = func() map[string]bool {
	raw := []string{
		"Set-Cookie",
		"Authorization",
		"WWW-Authenticate",
		"Proxy-Authenticate",
		"Strict-Transport-Security",
	}
	m := make(map[string]bool, len(raw))
	for _, h := range raw {
		m[http.CanonicalHeaderKey(h)] = true
	}
	return m
}()

// WithTTL sets the cache TTL for stored responses. Default: 24h.
//
// Panics on non-positive durations. The three idempotency stores disagree
// dangerously about TTL=0: Redis SET NX with EX 0 creates a permanent lock
// (no expiry), MemoryStore treats it as immediately expired, and pgstore
// rounds sub-second durations to 0. Rejecting at construction prevents the
// "works in tests, breaks Redis in prod" surprise.
func WithTTL(d time.Duration) Option {
	if d <= 0 {
		panic(fmt.Sprintf("idempotency: WithTTL requires a positive duration (got %s); zero/negative TTLs create permanent locks in Redis", d))
	}
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

// WithBodyFingerprint enables request-body fingerprinting. When enabled, the
// middleware buffers the request body (up to maxFingerprintBodySize), hashes
// it with SHA-256, and passes the digest to the Store. If a subsequent
// request reuses the same Idempotency-Key with a *different* body, the Store
// reports a mismatch and the middleware returns 422 Unprocessable Entity —
// the standard Stripe-style mitigation against "client retried with mutated
// body" silently corrupting state.
//
// Off by default for backward compatibility. Enable in any new deployment;
// it costs one SHA-256 hash + a buffered body per write request.
func WithBodyFingerprint() Option {
	return func(c *config) { c.fingerprintBody = true }
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
// In multi-tenant systems, you MUST use [WithUserExtractor] to scope
// idempotency keys per user. Otherwise different users sharing the same
// idempotency key would receive each other's cached responses — a classic
// account-takeover vector. Single-tenant or unauthenticated services that
// genuinely intend keys to be global must opt into the shared-key behaviour
// with [WithAllowSharedKeys]; the middleware panics at construction time
// when neither is set, matching the kit's fail-fast convention.
//
// Identity-bearing response headers (Set-Cookie, Authorization,
// WWW-Authenticate, Proxy-Authenticate, Strict-Transport-Security) are
// stripped from the cached response before storage, so a replay never
// re-emits another caller's session token or credential. Override the
// strip list with [WithPreserveHeaders] if your service legitimately
// needs to replay a header on this list.
func Middleware(store idem.Store, opts ...Option) func(http.Handler) http.Handler {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.userExtractor == nil && !cfg.allowSharedKeys {
		panic("idempotency: Middleware requires WithUserExtractor (multi-tenant safety) — pass WithAllowSharedKeys to opt out for single-tenant / unauthenticated services")
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

			var bodyFingerprint []byte
			if cfg.fingerprintBody {
				fp, body, fpErr := readAndFingerprintBody(r)
				if fpErr != nil {
					if cfg.metrics != nil {
						cfg.metrics.errors.Inc()
					}
					httpx.WriteError(w, http.StatusBadRequest, "could not read request body")
					return
				}
				bodyFingerprint = fp
				// Replace the request body so the downstream handler can
				// still read it.
				r.Body = io.NopCloser(bytes.NewReader(body))
			}

			cached, fpMismatch, err := store.Get(r.Context(), key, bodyFingerprint)
			if err != nil {
				if cfg.metrics != nil {
					cfg.metrics.errors.Inc()
				}
				httpx.WriteError(w, http.StatusInternalServerError, "idempotency store error")
				return
			}
			if fpMismatch {
				if cfg.metrics != nil {
					cfg.metrics.conflicts.Inc()
				}
				httpx.WriteError(w, http.StatusUnprocessableEntity,
					cfg.header+" reused with a different request body")
				return
			}
			if cached != nil {
				if cfg.metrics != nil {
					cfg.metrics.hits.Inc()
				}
				replay(w, cached)
				return
			}

			token, fpMismatchOnLock, locked, lockErr := store.TryLock(r.Context(), key, bodyFingerprint, cfg.ttl)
			if lockErr != nil {
				if cfg.metrics != nil {
					cfg.metrics.errors.Inc()
				}
				httpx.WriteError(w, http.StatusInternalServerError, "idempotency store error")
				return
			}
			if fpMismatchOnLock {
				if cfg.metrics != nil {
					cfg.metrics.conflicts.Inc()
				}
				httpx.WriteError(w, http.StatusUnprocessableEntity,
					cfg.header+" reused with a different request body")
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
					if unlockErr := store.Unlock(context.Background(), key, token); unlockErr != nil {
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
				if unlockErr := store.Unlock(context.Background(), key, token); unlockErr != nil {
					cfg.logger.Error("idempotency: failed to unlock after overflow",
						"error", unlockErr, "key", rawKey)
				}
				return
			}

			headers := make(map[string][]string, len(rec.Header()))
			for k, vals := range rec.Header() {
				if !cfg.preserveHeaders[k] && identityResponseHeaders[http.CanonicalHeaderKey(k)] {
					continue
				}
				cp := make([]string, len(vals))
				copy(cp, vals)
				headers[k] = cp
			}
			resp := idem.CachedResponse{
				StatusCode: rec.statusCode,
				Headers:    headers,
				Body:       append([]byte(nil), rec.body.Bytes()...),
			}
			if setErr := store.Set(context.Background(), key, token, resp, cfg.ttl); setErr != nil {
				if errors.Is(setErr, idem.ErrLockLost) {
					// TTL expired and another caller has taken the slot —
					// don't fight them. Their response will be the one
					// future requests replay.
					cfg.logger.Warn("idempotency: lock lost before Set; another caller now owns the slot",
						"key", rawKey)
				} else {
					cfg.logger.Error("idempotency: failed to cache response, lock held until TTL expiry",
						"error", setErr, "key", rawKey)
					// Do NOT unlock — keeping the lock prevents duplicate execution
					// during the TTL window. The lock expires naturally.
				}
			}
			// On success, Set has already replaced the lock with the response;
			// no separate Unlock is needed.
		})
	}
}

// readAndFingerprintBody buffers the request body up to maxFingerprintBodySize,
// computes a SHA-256 digest, and returns both the digest and the buffered
// body so the caller can install a fresh reader before forwarding.
func readAndFingerprintBody(r *http.Request) ([]byte, []byte, error) {
	if r.Body == nil {
		// Empty body still gets a stable fingerprint so empty-body retries
		// match each other.
		empty := sha256.Sum256(nil)
		return empty[:], nil, nil
	}
	limited := io.LimitReader(r.Body, maxFingerprintBodySize+1)
	body, err := io.ReadAll(limited)
	if cerr := r.Body.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxFingerprintBodySize {
		// Body too big to fingerprint; fall back to length-prefixed sentinel
		// so length-mismatch alone is still detectable.
		h := sha256.New()
		_, _ = io.WriteString(h, "rho-kit:idempotency:body-too-large")
		return h.Sum(nil), body, nil
	}
	digest := sha256.Sum256(body)
	return digest[:], body, nil
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

// WithAllowSharedKeys opts a service into the unsafe behaviour of NOT
// scoping idempotency keys per user. Use only for genuinely single-tenant
// services or unauthenticated endpoints (webhook receivers from a known
// counterparty, public RSS, etc.) where one user replaying another's
// response is impossible by construction.
func WithAllowSharedKeys() Option {
	return func(c *config) { c.allowSharedKeys = true }
}

// WithPreserveHeaders adds headers to the allowlist of response headers that
// MAY be cached and replayed. The middleware strips identity-bearing
// headers (Set-Cookie, Authorization, WWW-Authenticate, Proxy-Authenticate,
// Strict-Transport-Security) by default so a cached response cannot leak
// another user's session token. Use this option only when the application
// legitimately replays one of those headers across calls — e.g. a stable
// HSTS policy that's identical for every response and you want to avoid the
// browser missing it on a replay (rare).
//
// Header names are matched after http.CanonicalHeaderKey normalisation.
func WithPreserveHeaders(names ...string) Option {
	return func(c *config) {
		if c.preserveHeaders == nil {
			c.preserveHeaders = make(map[string]bool, len(names))
		}
		for _, n := range names {
			c.preserveHeaders[http.CanonicalHeaderKey(n)] = true
		}
	}
}
