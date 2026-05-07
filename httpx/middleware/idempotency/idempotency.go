package idempotency

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/http/httpguts"

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

// maxFingerprintBodySize is the largest request body the middleware will
// buffer when fingerprinting is enabled. Requests with larger bodies are
// rejected with 413 Payload Too Large rather than truncated — silently
// truncating would let a downstream handler observe a partial body while
// returning success, and hashing a constant "too-large" sentinel would
// collapse every oversized body to the same fingerprint and defeat the
// body-mismatch protection the option exists to provide.
const maxFingerprintBodySize = 1 << 20 // 1 MiB

// defaultPostHandlerTimeout bounds the post-handler Set/Unlock store calls
// so a hung backend cannot pin request goroutines indefinitely after the
// handler has already returned. Tunable via [WithPostHandlerTimeout].
const defaultPostHandlerTimeout = 5 * time.Second

type config struct {
	userExtractor      func(*http.Request) string
	ttl                time.Duration
	header             string
	requiredMethods    map[string]bool
	logger             *slog.Logger
	metrics            *Metrics
	fingerprintBody    bool
	allowSharedKeys    bool
	preserveHeaders    map[string]bool // optional override of identityResponseHeaders
	postHandlerTimeout time.Duration
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
// Panics on non-positive durations. Backend precision varies — pgstore
// rounds sub-second durations up to 1 second (PostgreSQL interval column
// precision), redisstore rounds sub-millisecond durations up to 1ms (Redis
// PX precision), MemoryStore is nanosecond-precise. The middleware rejects
// non-positive TTLs at construction so callers cannot construct a
// "permanent lock" (Redis SET NX with EX 0) by mistake.
func WithTTL(d time.Duration) Option {
	if d <= 0 {
		panic(fmt.Sprintf("idempotency: WithTTL requires a positive duration (got %s); zero/negative TTLs create permanent locks in Redis", d))
	}
	return func(c *config) { c.ttl = d }
}

// WithHeader sets the header name used as idempotency key. Default: "Idempotency-Key".
// Panics if name is empty or not a valid HTTP header field name — an invalid
// header name would make every request fail with a confusing missing-header error.
func WithHeader(name string) Option {
	if !httpguts.ValidHeaderFieldName(name) {
		panic(fmt.Sprintf("idempotency: WithHeader requires a valid HTTP header field name (got %q)", name))
	}
	return func(c *config) { c.header = name }
}

// WithLogger sets the logger for idempotency store errors. A nil logger is
// normalized to slog.Default() so error paths cannot panic.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l == nil {
			l = slog.Default()
		}
		c.logger = l
	}
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

// WithBodyFingerprint enables request-body fingerprinting. Body
// fingerprinting is ON by default, so this option is a no-op kept for
// backward compatibility with callers that pass it explicitly. Use
// [WithoutBodyFingerprint] to opt out.
//
// When enabled, the middleware buffers the request body (up to
// [maxFingerprintBodySize]), hashes it with SHA-256, and passes the digest
// to the Store. If a subsequent request reuses the same Idempotency-Key
// with a *different* body, the Store reports a mismatch and the middleware
// returns 422 Unprocessable Entity — the standard Stripe-style mitigation
// against "client retried with mutated body" silently corrupting state.
//
// Requests whose body exceeds [maxFingerprintBodySize] are rejected with
// 413 Payload Too Large: silently truncating would forward a partial body
// to the handler while still appearing to succeed, and any constant
// "too-large" sentinel would collapse every oversized body to the same
// fingerprint and defeat body-mismatch protection. Services that legitimately
// accept multi-megabyte writes should pass [WithoutBodyFingerprint] on
// those routes.
func WithBodyFingerprint() Option {
	return func(c *config) { c.fingerprintBody = true }
}

// WithoutBodyFingerprint disables request-body fingerprinting. Body
// fingerprinting is ON by default because the same idempotency key reused
// with a different body is the main corruption case the middleware exists
// to prevent: without fingerprinting the second request would silently hit
// the previous response (or share its lock) even though the caller's intent
// changed. Opt out only on routes that knowingly accept multi-megabyte
// bodies (the buffer cap rejects those with 413) and that have an
// out-of-band guarantee no caller will reuse a key with a mutated body.
func WithoutBodyFingerprint() Option {
	return func(c *config) { c.fingerprintBody = false }
}

// WithPostHandlerTimeout sets the deadline for the Set/Unlock store calls
// the middleware makes after the handler has returned. Default: 5s.
//
// These calls run with a fresh background context (the request context is
// already cancelled by the time the handler returns), so a hung Redis or
// Postgres backend without this bound would pin a goroutine until the TCP
// timeout fires. Panics on non-positive durations.
func WithPostHandlerTimeout(d time.Duration) Option {
	if d <= 0 {
		panic(fmt.Sprintf("idempotency: WithPostHandlerTimeout requires a positive duration (got %s)", d))
	}
	return func(c *config) { c.postHandlerTimeout = d }
}

// defaultConfig returns the default middleware configuration. Body
// fingerprinting defaults to ON: the "same key, different body" corruption
// case is what the middleware exists to prevent, and silently falling back
// to the previous response on a mutated retry is the failure mode opt-in
// defaults consistently produced. Routes that legitimately accept
// multi-megabyte bodies should pass [WithoutBodyFingerprint].
func defaultConfig() config {
	return config{
		ttl:                24 * time.Hour,
		header:             "Idempotency-Key",
		logger:             slog.Default(),
		postHandlerTimeout: defaultPostHandlerTimeout,
		fingerprintBody:    true,
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
// Extractor contract: when [WithUserExtractor] is set, the extractor MUST
// return a non-empty user identifier for every request that reaches this
// middleware. If the extractor returns "" the request is rejected with
// HTTP 400 ("idempotency requires authenticated request") and no cache
// slot is touched — collapsing to a (method, path, rawKey)-only key would
// silently let an anonymous request share a slot with another anonymous
// (or worse, a logged-in but extractor-failed) caller, exposing the
// previous response body via Idempotency-Key replay. Route any
// anonymous-eligible requests around this middleware (or behind a path
// that does NOT require an Idempotency-Key) instead of relying on a
// "sometimes returns user, sometimes returns empty" extractor.
//
// Identity-bearing response headers (Set-Cookie, Authorization,
// WWW-Authenticate, Proxy-Authenticate, Strict-Transport-Security) are
// stripped from the cached response before storage, so a replay never
// re-emits another caller's session token or credential. Override the
// strip list with [WithPreserveHeaders] if your service legitimately
// needs to replay a header on this list.
func Middleware(store idem.Store, opts ...Option) func(http.Handler) http.Handler {
	if store == nil {
		panic("idempotency: Middleware requires a non-nil Store")
	}
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
				if userID == "" {
					// Fail closed: collapsing to (method, path, rawKey) here
					// would let an anonymous request share a cache slot with
					// another anonymous (or extractor-failed) caller and
					// replay the previous response body via the same key.
					httpx.WriteError(w, http.StatusBadRequest,
						"idempotency requires authenticated request")
					return
				}
			}
			key := fingerprintKey(r.Method, r.URL.Path, rawKey, userID)

			var bodyFingerprint []byte
			if cfg.fingerprintBody {
				fp, body, fpErr := readAndFingerprintBody(r)
				if fpErr != nil {
					if errors.Is(fpErr, errBodyTooLarge) {
						httpx.WriteError(w, http.StatusRequestEntityTooLarge,
							fmt.Sprintf("request body exceeds idempotency fingerprint limit (%d bytes)", maxFingerprintBodySize))
						return
					}
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
					ctx, cancel := context.WithTimeout(context.Background(), cfg.postHandlerTimeout)
					defer cancel()
					if unlockErr := store.Unlock(ctx, key, token); unlockErr != nil {
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
				ctx, cancel := context.WithTimeout(context.Background(), cfg.postHandlerTimeout)
				defer cancel()
				if unlockErr := store.Unlock(ctx, key, token); unlockErr != nil {
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
			setCtx, setCancel := context.WithTimeout(context.Background(), cfg.postHandlerTimeout)
			defer setCancel()
			if setErr := store.Set(setCtx, key, token, resp, cfg.ttl); setErr != nil {
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

// errBodyTooLarge signals that the request body exceeded
// [maxFingerprintBodySize] when fingerprinting is enabled. The middleware
// translates this into 413 Payload Too Large rather than silently truncating
// the body or hashing a constant sentinel — both alternatives would let
// different oversized bodies share an idempotency slot.
var errBodyTooLarge = errors.New("idempotency: request body exceeds fingerprint limit")

// readAndFingerprintBody buffers the request body up to maxFingerprintBodySize,
// computes a SHA-256 digest, and returns both the digest and the buffered
// body so the caller can install a fresh reader before forwarding. Returns
// [errBodyTooLarge] when the body exceeds the cap.
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
		return nil, nil, errBodyTooLarge
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

// Flush forwards to the underlying ResponseWriter when it implements
// http.Flusher. Streaming handlers (SSE, chunked transfer) rely on Flush
// reaching the wire; without this delegation the wrapper would silently
// swallow the call.
func (rc *responseCapture) Flush() {
	if !rc.wroteHeader {
		rc.WriteHeader(http.StatusOK)
	}
	if f, ok := rc.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying ResponseWriter when it implements
// http.Hijacker. After hijack the response capture is meaningless, so we
// flag bodyOverflow to suppress caching of whatever bytes we already
// captured — the caller has taken control of the connection.
func (rc *responseCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rc.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("idempotency: underlying ResponseWriter does not implement http.Hijacker")
	}
	rc.bodyOverflow = true
	return h.Hijack()
}

// Push forwards to the underlying ResponseWriter when it implements
// http.Pusher (HTTP/2 server push). Returns http.ErrNotSupported when the
// inner writer cannot push, matching the standard library behaviour.
func (rc *responseCapture) Push(target string, opts *http.PushOptions) error {
	if p, ok := rc.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// ReadFrom lets handlers using io.Copy take the optimised sendfile path
// when the underlying writer is an io.ReaderFrom (e.g. *http.response).
// We still tee bytes into the capture buffer so the cached replay is
// faithful, falling back to the generic path once the body cap is hit.
func (rc *responseCapture) ReadFrom(src io.Reader) (int64, error) {
	rf, ok := rc.ResponseWriter.(io.ReaderFrom)
	if !ok {
		return io.Copy(writerOnly{rc}, src)
	}
	if !rc.wroteHeader {
		rc.WriteHeader(http.StatusOK)
	}
	if rc.bodyOverflow {
		return rf.ReadFrom(src)
	}
	return rf.ReadFrom(io.TeeReader(src, &captureSink{rc: rc}))
}

// writerOnly hides ReadFrom from io.Copy so the fallback in [responseCapture.ReadFrom]
// uses the generic copy loop and does not re-enter ReadFrom.
type writerOnly struct{ io.Writer }

// captureSink mirrors bytes written through ReadFrom into the capture buffer
// while honouring the same overflow rule as Write.
type captureSink struct{ rc *responseCapture }

func (s *captureSink) Write(b []byte) (int, error) {
	if s.rc.bodyOverflow {
		return len(b), nil
	}
	if s.rc.body.Len()+len(b) > maxCapturedBodySize {
		s.rc.bodyOverflow = true
		s.rc.body.Reset()
		return len(b), nil
	}
	s.rc.body.Write(b)
	return len(b), nil
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
