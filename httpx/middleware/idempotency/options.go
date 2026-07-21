package idempotency

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Option configures the idempotency middleware.
type Option func(*config)

// Metrics holds Prometheus counters for idempotency middleware observability.
type Metrics struct {
	hits      prometheus.Counter // cached response replayed
	misses    prometheus.Counter // no cached response, processing
	conflicts prometheus.Counter // key in flight (409) or reused with a different body (422)
	errors    prometheus.Counter // store Get/TryLock/Set faults (500); client body-read failures are NOT counted here
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
		panic("middleware/idempotency: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers idempotency metrics. Pass
// [WithRegisterer] to use a non-default registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("middleware/idempotency: NewMetrics option must not be nil")
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
	lockTTL            time.Duration // processing-lock TTL; 0 means "use ttl"
	header             string
	requiredMethods    map[string]bool
	logger             *slog.Logger
	metrics            *Metrics
	fingerprintBody    bool
	allowSharedKeys    bool
	preserveHeaders    map[string]bool // optional override of identityResponseHeaders
	uncachedStatuses   map[int]bool    // statuses released (not cached) after the handler
	postHandlerTimeout time.Duration
	optionalKey        bool   // pass through when Idempotency-Key absent on required methods
	replayHeader       string // response header set to "true" on cache replay; empty disables
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
		panic("middleware/idempotency: WithTTL requires a positive duration; zero/negative TTLs create permanent locks in Redis")
	}
	return func(c *config) { c.ttl = d }
}

// WithLockTTL sets a separate, typically short, TTL for the processing lock
// acquired before the handler runs. Unset, the processing lock inherits the
// response-cache TTL ([WithTTL], default 24h).
//
// The two TTLs serve different purposes. The cache TTL governs how long a
// completed response is replayed. The processing lock only needs to outlive a
// single in-flight handler: its deferred Unlock releases the lock on normal
// completion and on panic, but a hard crash (kill -9, OOM, node loss) mid
// handler cannot run the deferred Unlock, so the lock lingers until its TTL
// expires. With a shared 24h TTL that crash locks the key out — every retry
// gets 409 "request already in progress" — for a full day. A short lock TTL
// (Stripe-style, e.g. 30-60s) lets retries recover quickly after a crash while
// the cache TTL stays long.
//
// Pick a lock TTL comfortably above the slowest expected handler latency so a
// legitimately slow handler does not lose its lock mid-flight (which surfaces
// as [idem.ErrLockLost] on Set). Panics on non-positive durations.
func WithLockTTL(d time.Duration) Option {
	if d <= 0 {
		panic("middleware/idempotency: WithLockTTL requires a positive duration")
	}
	return func(c *config) { c.lockTTL = d }
}

// WithUncachedStatuses lists HTTP status codes whose responses are NOT cached.
// After the handler returns one of these statuses the middleware releases the
// processing lock instead of storing the response, so a subsequent retry with
// the same Idempotency-Key re-executes the handler rather than replaying the
// earlier response for the full cache TTL.
//
// The default behaviour caches every status, including transient 500/502/503
// from backend failures, and replays them for up to the cache TTL (default
// 24h) — a client that correctly retries with the same key can never recover
// from a transient error inside that window. Pass the transient/error statuses
// your service wants retries to recover from, e.g.:
//
//	WithUncachedStatuses(http.StatusBadGateway, http.StatusServiceUnavailable,
//	    http.StatusGatewayTimeout)
//
// Only cache the statuses that are genuinely safe to replay (typically 2xx and
// deliberate 4xx). Releasing the lock means concurrent retries of the same key
// may both run the handler, so callers that opt in accept at-least-once
// execution for the listed statuses — which is exactly the right trade-off for
// transient failures the caller wants to retry. Panics on status codes outside
// the 100-599 range.
func WithUncachedStatuses(statuses ...int) Option {
	set := make(map[int]bool, len(statuses))
	for _, s := range statuses {
		if s < 100 || s > 599 {
			panic("middleware/idempotency: WithUncachedStatuses requires valid HTTP status codes (100-599)")
		}
		set[s] = true
	}
	return func(c *config) {
		if c.uncachedStatuses == nil {
			c.uncachedStatuses = make(map[int]bool, len(set))
		}
		for s := range set {
			c.uncachedStatuses[s] = true
		}
	}
}

// WithHeader sets the header name used as idempotency key. Default: "Idempotency-Key".
// Panics if name is empty or not a valid HTTP header field name — an invalid
// header name would make every request fail with a confusing missing-header error.
func WithHeader(name string) Option {
	if !httpguts.ValidHeaderFieldName(name) {
		panic("middleware/idempotency: WithHeader requires a valid HTTP header field name")
	}
	return func(c *config) { c.header = name }
}

// WithLogger sets the logger for idempotency store errors.
// Panics if l is nil — omit the option to keep slog.Default(), matching
// the kit's dominant middleware WithLogger contract (signedrequest,
// timeout, auditlog).
func WithLogger(l *slog.Logger) Option {
	if l == nil {
		panic("middleware/idempotency: WithLogger requires a non-nil logger (omit the option to use slog.Default)")
	}
	return func(c *config) { c.logger = l }
}

// WithMetrics enables Prometheus metrics for the middleware.
func WithMetrics(m *Metrics) Option {
	if m == nil {
		panic("middleware/idempotency: WithMetrics requires non-nil Metrics")
	}
	return func(c *config) { c.metrics = m }
}

// WithRequiredMethods sets the HTTP methods that require an
// idempotency key. Default: POST, PUT, PATCH.
//
// Panics on a zero-length call (no methods). The v1 shape silently
// accepted `WithRequiredMethods()` and replaced the safe default with
// an empty map — middleware would then bypass every request. Services
// that intentionally want no required methods must opt in via
// [WithoutRequiredMethods].
func WithRequiredMethods(methods ...string) Option {
	if len(methods) == 0 {
		panic("middleware/idempotency: WithRequiredMethods requires at least one method (use WithoutRequiredMethods to disable enforcement explicitly)")
	}
	canonical := make([]string, 0, len(methods))
	for _, m := range methods {
		m = strings.ToUpper(strings.TrimSpace(m))
		if !httpguts.ValidHeaderFieldName(m) {
			panic("middleware/idempotency: WithRequiredMethods requires valid HTTP method tokens")
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

// WithOptionalKey allows POST/PUT/PATCH without an Idempotency-Key to
// pass through unchanged. Only a fully absent header bypasses the store;
// a present but empty value still fails validation like any other invalid key.
// When the header is absent the middleware does not touch the store; when
// present, normal deduplication applies. Pair with [WithRequiredMethods]
// (the default) for opt-in idempotency on mutating routes when clients send
// Idempotency-Key only when needed.
func WithOptionalKey() Option {
	return func(c *config) { c.optionalKey = true }
}

// WithReplayHeader sets the response header name emitted on cache replay.
// The header value is always "true". Default: off (replay is transparent).
// Panics if name is empty or not a valid HTTP header field name.
func WithReplayHeader(name string) Option {
	if !httpguts.ValidHeaderFieldName(name) {
		panic("middleware/idempotency: WithReplayHeader requires a valid HTTP header field name")
	}
	return func(c *config) { c.replayHeader = http.CanonicalHeaderKey(name) }
}

// WithoutRequiredMethods disables the "method requires an idempotency
// key" enforcement entirely. With no required methods every request —
// including mutating ones — takes the early pass-through: the middleware
// performs NO deduplication or caching, and an Idempotency-Key header a
// client sends anyway is ignored, not honoured opportunistically. (This
// is also why non-required methods under the default config carry their
// key straight through.) The long, explicit name is deliberate: this is
// the unsafe-by-default escape hatch that turns off the middleware's
// main protection. Use only when the caller has an out-of-band reason
// (an upstream gateway already enforces idempotency, or the routes
// behind this middleware are genuinely safe to retry).
func WithoutRequiredMethods() Option {
	return func(c *config) {
		c.requiredMethods = map[string]bool{}
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
		panic("middleware/idempotency: WithPostHandlerTimeout requires a positive duration")
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

// lockOrCacheTTL resolves the TTL used for the processing lock. When no
// dedicated lock TTL is configured ([WithLockTTL]) the lock inherits the
// response-cache TTL, preserving the historical single-TTL behaviour.
func (c config) lockOrCacheTTL() time.Duration {
	if c.lockTTL > 0 {
		return c.lockTTL
	}
	return c.ttl
}
