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
	"net/url"
	"runtime/debug"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/core/v2/redact"
	idem "github.com/bds421/rho-kit/data/v2/idempotency"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/observability/v2/promutil"
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

// MetricsOption configures the idempotency metric constructor.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for idempotency
// metrics. Unset defaults to [prometheus.DefaultRegisterer]; passing
// nil panics.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("idempotency: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers idempotency metrics. Pass
// [WithRegisterer] to use a non-default registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("idempotency: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer
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
	m.hits = promutil.MustRegisterOrGet(reg, m.hits)
	m.misses = promutil.MustRegisterOrGet(reg, m.misses)
	m.conflicts = promutil.MustRegisterOrGet(reg, m.conflicts)
	m.errors = promutil.MustRegisterOrGet(reg, m.errors)
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
	// semanticHeaders are HTTP headers folded into the fingerprint
	// key (audit FR-029). Use this for headers whose value affects
	// the request semantics in ways the kit cannot infer — typically
	// X-Tenant-Id, X-Org-Id, or any custom routing header.
	// Default empty: only method, path, query, raw key, and user ID
	// participate. Configure via [WithSemanticHeaders].
	semanticHeaders []string
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
		panic("idempotency: WithTTL requires a positive duration; zero/negative TTLs create permanent locks in Redis")
	}
	return func(c *config) { c.ttl = d }
}

// WithHeader sets the header name used as idempotency key. Default: "Idempotency-Key".
// Panics if name is empty or not a valid HTTP header field name — an invalid
// header name would make every request fail with a confusing missing-header error.
func WithHeader(name string) Option {
	if !httpguts.ValidHeaderFieldName(name) {
		panic("idempotency: WithHeader requires a valid HTTP header field name")
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
	if m == nil {
		panic("idempotency: WithMetrics requires non-nil Metrics")
	}
	return func(c *config) { c.metrics = m }
}

// WithRequiredMethods sets the HTTP methods that require an idempotency key.
// Default: POST, PUT, PATCH.
func WithRequiredMethods(methods ...string) Option {
	canonical := make([]string, 0, len(methods))
	for _, m := range methods {
		m = strings.ToUpper(strings.TrimSpace(m))
		if !httpguts.ValidHeaderFieldName(m) {
			panic("idempotency: WithRequiredMethods requires valid HTTP method tokens")
		}
		canonical = append(canonical, m)
	}
	return func(c *config) {
		c.requiredMethods = make(map[string]bool, len(methods))
		for _, m := range canonical {
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
// These calls run on a cancellation-detached copy of the request context with
// a short timeout, so they survive client disconnects while preserving request
// context values for tenant-aware stores, tracing, and logging. A hung Redis or
// Postgres backend without this bound would pin a goroutine until the TCP
// timeout fires. Panics on non-positive durations.
func WithPostHandlerTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("idempotency: WithPostHandlerTimeout requires a positive duration")
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
		if o == nil {
			panic("idempotency: Middleware option must not be nil")
		}
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

			rawKey, ok := singleHeaderValue(r.Header, cfg.header)
			if !ok {
				httpx.WriteError(w, http.StatusBadRequest, "idempotency key is required exactly once")
				return
			}
			if strings.Contains(rawKey, ",") {
				httpx.WriteError(w, http.StatusBadRequest, "idempotency key is invalid")
				return
			}
			if err := idem.ValidateKey(rawKey); err != nil {
				httpx.WriteError(w, http.StatusBadRequest, "idempotency key is invalid")
				return
			}

			userID := ""
			if cfg.userExtractor != nil {
				var ok bool
				userID, ok = safeUserExtractor(cfg.logger, cfg.userExtractor, r)
				if !ok {
					httpx.WriteError(w, http.StatusBadRequest,
						"idempotency requires authenticated request")
					return
				}
				if userID == "" {
					// Fail closed: collapsing to (method, path, rawKey) here
					// would let an anonymous request share a cache slot with
					// another anonymous (or extractor-failed) caller and
					// replay the previous response body via the same key.
					httpx.WriteError(w, http.StatusBadRequest,
						"idempotency requires authenticated request")
					return
				}
				if err := idem.ValidateKey(userID); err != nil {
					httpx.WriteError(w, http.StatusBadRequest,
						"idempotency requires valid authenticated identity")
					return
				}
			}
			// FR-029 [HIGH]: include canonical query string and any
			// configured semantic headers in the fingerprint so two
			// requests that differ on query/header (e.g., dry_run=true vs
			// false) do not collide on the same body+key.
			key, keyErr := fingerprintKey(r, rawKey, userID, cfg.semanticHeaders)
			if keyErr != nil {
				httpx.WriteError(w, http.StatusBadRequest,
					"configured semantic idempotency headers are required exactly once")
				return
			}

			var bodyFingerprint []byte
			if cfg.fingerprintBody {
				fp, body, fpErr := readAndFingerprintBody(r)
				if fpErr != nil {
					if errors.Is(fpErr, errBodyTooLarge) {
						httpx.WriteError(w, http.StatusRequestEntityTooLarge,
							fmt.Sprintf("request body exceeds idempotency fingerprint limit (%d bytes)", maxFingerprintBodySize))
						return
					}
					if errors.Is(fpErr, errInvalidFingerprintHeader) {
						httpx.WriteError(w, http.StatusBadRequest,
							"idempotency fingerprint headers are invalid")
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
					"idempotency key reused with a different request body")
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
					"idempotency key reused with a different request body")
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
					ctx, cancel := postHandlerContext(r.Context(), cfg.postHandlerTimeout)
					defer cancel()
					if unlockErr := store.Unlock(ctx, key, token); unlockErr != nil {
						cfg.logger.Error("idempotency: failed to unlock after panic",
							redact.Error(unlockErr), redact.String("key", rawKey))
					}
				}
			}()

			next.ServeHTTP(rec, r)
			panicked = false

			if rec.bodyOverflow {
				cfg.logger.Warn("idempotency: response too large to cache, skipping",
					redact.String("key", rawKey))
				ctx, cancel := postHandlerContext(r.Context(), cfg.postHandlerTimeout)
				defer cancel()
				if unlockErr := store.Unlock(ctx, key, token); unlockErr != nil {
					cfg.logger.Error("idempotency: failed to unlock after overflow",
						redact.Error(unlockErr), redact.String("key", rawKey))
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
			setCtx, setCancel := postHandlerContext(r.Context(), cfg.postHandlerTimeout)
			defer setCancel()
			if setErr := store.Set(setCtx, key, token, resp, cfg.ttl); setErr != nil {
				if errors.Is(setErr, idem.ErrLockLost) {
					// TTL expired and another caller has taken the slot —
					// don't fight them. Their response will be the one
					// future requests replay.
					cfg.logger.Warn("idempotency: lock lost before Set; another caller now owns the slot",
						redact.String("key", rawKey))
				} else {
					cfg.logger.Error("idempotency: failed to cache response, lock held until TTL expiry",
						redact.Error(setErr), redact.String("key", rawKey))
					// Do NOT unlock — keeping the lock prevents duplicate execution
					// during the TTL window. The lock expires naturally.
				}
			}
			// On success, Set has already replaced the lock with the response;
			// no separate Unlock is needed.
		})
	}
}

func postHandlerContext(reqCtx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(reqCtx), timeout)
}

func safeUserExtractor(logger *slog.Logger, fn func(*http.Request) string, r *http.Request) (userID string, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			if logger == nil {
				logger = slog.Default()
			}
			logger.Error("idempotency: user extractor panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			userID, ok = "", false
		}
	}()
	return fn(r), true
}

func singleHeaderValue(h http.Header, name string) (string, bool) {
	values := h.Values(name)
	if len(values) != 1 {
		return "", false
	}
	value := values[0]
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || !httpguts.ValidHeaderFieldValue(value) {
		return "", false
	}
	return value, true
}

// errBodyTooLarge signals that the request body exceeded
// [maxFingerprintBodySize] when fingerprinting is enabled. The middleware
// translates this into 413 Payload Too Large rather than silently truncating
// the body or hashing a constant sentinel — both alternatives would let
// different oversized bodies share an idempotency slot.
var errBodyTooLarge = errors.New("idempotency: request body exceeds fingerprint limit")

var errInvalidFingerprintHeader = errors.New("idempotency: invalid fingerprint header")

var bodyFingerprintHeaders = [...]string{"Content-Type", "Content-Encoding"}

// readAndFingerprintBody buffers the request body up to maxFingerprintBodySize,
// computes a SHA-256 digest, and returns both the digest and the buffered
// body so the caller can install a fresh reader before forwarding. Returns
// [errBodyTooLarge] when the body exceeds the cap.
func readAndFingerprintBody(r *http.Request) ([]byte, []byte, error) {
	headers, err := bodySemanticHeaders(r)
	if err != nil {
		return nil, nil, err
	}
	if r.Body == nil {
		// Empty body still gets a stable fingerprint so empty-body retries
		// match each other.
		return requestBodyFingerprint(headers, nil), nil, nil
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
	return requestBodyFingerprint(headers, body), body, nil
}

func bodySemanticHeaders(r *http.Request) (map[string]string, error) {
	out := make(map[string]string, len(bodyFingerprintHeaders))
	for _, name := range bodyFingerprintHeaders {
		value, err := optionalSingletonHeaderValue(r.Header, name)
		if err != nil {
			return nil, err
		}
		if value != "" {
			out[name] = value
		}
	}
	return out, nil
}

func requestBodyFingerprint(headers map[string]string, body []byte) []byte {
	h := sha256.New()
	_, _ = io.WriteString(h, "rho-kit-idempotency-body-v2")
	for _, name := range bodyFingerprintHeaders {
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, name)
		_, _ = io.WriteString(h, ":")
		_, _ = io.WriteString(h, headers[name])
	}
	_, _ = io.WriteString(h, "\x00")
	_, _ = h.Write(body)
	return h.Sum(nil)
}

func optionalSingletonHeaderValue(h http.Header, name string) (string, error) {
	values := h.Values(name)
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("%w: header must appear at most once", errInvalidFingerprintHeader)
	}
	value := values[0]
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || !httpguts.ValidHeaderFieldValue(value) {
		return "", fmt.Errorf("%w: header has invalid value", errInvalidFingerprintHeader)
	}
	return value, nil
}

// fingerprintKey builds the cache key from the dimensions that
// MUST be the same across two requests for them to share an
// idempotent reply: method, path, canonical query string, the raw
// idempotency-key header, the resolved user ID, and any configured
// semantic headers (audit FR-029).
//
// The canonicalization rules:
//   - Query parameters are sorted by name and re-serialised so that
//     ?b=1&a=2 and ?a=2&b=1 (semantically identical) hash equally.
//   - Configured semantic headers must be present exactly once with a
//     non-blank value. Duplicate/missing values are rejected instead of
//     joined, because "a,b" and ["a","b"] would otherwise collide.
//
// Components are separated by NUL bytes — the byte that cannot
// appear in HTTP method/path/key tokens — so concatenation can never
// alias one input into another.
func fingerprintKey(r *http.Request, rawKey, userID string, semanticHeaders []string) (string, error) {
	h := sha256.New()
	_, _ = io.WriteString(h, r.Method)
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, canonicalRequestPath(r.URL))
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, canonicalQuery(r.URL.Query()))
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, rawKey)
	if userID != "" {
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, userID)
	}
	for _, name := range semanticHeaders {
		_, _ = io.WriteString(h, "\x00")
		// Header name is case-insensitive on the wire — fold to
		// canonical so the configured "X-Tenant-Id" matches
		// http.Header's normalized form.
		canonical := http.CanonicalHeaderKey(name)
		value, ok := singleHeaderValue(r.Header, canonical)
		if !ok {
			return "", fmt.Errorf("idempotency: semantic header is required exactly once")
		}
		_, _ = io.WriteString(h, canonical)
		_, _ = io.WriteString(h, "=")
		_, _ = io.WriteString(h, value)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func canonicalRequestPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.EscapedPath()
}

// canonicalQuery serializes a url.Values with deterministic key
// ordering. Two requests whose query strings differ only in
// parameter order produce identical canonical forms.
func canonicalQuery(v url.Values) string {
	if len(v) == 0 {
		return ""
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := url.Values{}
	for _, k := range keys {
		out[k] = v[k]
	}
	return out.Encode()
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
	if fn == nil {
		panic("idempotency: WithUserExtractor requires a non-nil extractor")
	}
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

// WithSemanticHeaders folds the named request headers into the
// idempotency fingerprint so two requests with the same body and key
// but different header values do NOT collide on the same cache slot.
// The audit (FR-029) flagged this for headers like X-Tenant-Id,
// X-Org-Id, X-Region, or X-Dry-Run where the value materially changes
// the request's effect. Without this option the middleware would
// happily replay a tenant-A response for a tenant-B request that
// happens to share the same Idempotency-Key — a cross-tenant data leak.
//
// Header names are case-insensitive and folded to canonical form on
// match. Pass each header that affects request semantics; do NOT pass
// auth headers (Authorization, Cookie) — those should be reflected
// through [WithUserExtractor] instead so the fingerprint stays stable
// across token rotations for the same identity.
//
// Configured order is preserved (not sorted) when joining values for
// the digest, so the operator decides whether to treat
// X-Tenant-Id: a vs X-Tenant-Id: a,b as distinct.
func WithSemanticHeaders(names ...string) Option {
	canonical := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if !httpguts.ValidHeaderFieldName(n) {
			panic("idempotency: WithSemanticHeaders requires a valid HTTP header field name")
		}
		canonical = append(canonical, http.CanonicalHeaderKey(n))
	}
	return func(c *config) {
		c.semanticHeaders = append(c.semanticHeaders, canonical...)
	}
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
	// FR-032 [LOW]: validate header names at construction so a typo
	// or invalid character does not silently no-op at request time.
	canonical := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if !httpguts.ValidHeaderFieldName(n) {
			panic("idempotency: WithPreserveHeaders requires a valid HTTP header field name")
		}
		canonical = append(canonical, http.CanonicalHeaderKey(n))
	}
	return func(c *config) {
		if c.preserveHeaders == nil {
			c.preserveHeaders = make(map[string]bool, len(canonical))
		}
		for _, n := range canonical {
			c.preserveHeaders[n] = true
		}
	}
}
