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
	"net/url"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/core/v2/redact"
	idem "github.com/bds421/rho-kit/data/v2/idempotency"
	"github.com/bds421/rho-kit/httpx/v2"
)

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
		panic("middleware/idempotency: Middleware requires a non-nil Store")
	}
	cfg := defaultConfig()
	for _, o := range opts {
		if o == nil {
			panic("middleware/idempotency: Middleware option must not be nil")
		}
		o(&cfg)
	}

	if cfg.userExtractor == nil && !cfg.allowSharedKeys {
		panic("middleware/idempotency: Middleware requires WithUserExtractor (multi-tenant safety) — pass WithAllowSharedKeys to opt out for single-tenant / unauthenticated services")
	}

	g := &guard{store: store, cfg: &cfg}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			g.serve(w, r, next)
		})
	}
}

// guard is the configured middleware over its store.
type guard struct {
	store idem.Store
	cfg   *config
}

// slot is one request's place in the store: its store key, the raw
// Idempotency-Key the logs name and the request body's fingerprint.
type slot struct {
	key         string
	rawKey      string
	fingerprint []byte
}

func (g *guard) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if !g.cfg.requiredMethods[r.Method] {
		next.ServeHTTP(w, r)
		return
	}
	rawKey, ok := singleHeaderValue(r.Header, g.cfg.header)
	if !ok {
		if g.cfg.optionalKey && len(r.Header.Values(g.cfg.header)) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, "idempotency key is required exactly once")
		return
	}
	if strings.Contains(rawKey, ",") || idem.ValidateKey(rawKey) != nil {
		httpx.WriteError(w, http.StatusBadRequest, "idempotency key is invalid")
		return
	}
	s, ok := g.requestSlot(w, r, rawKey)
	if !ok {
		return
	}
	if g.replayed(w, r, s, "idempotency: store Get failed") {
		return
	}
	token, ok := g.lock(w, r, s)
	if !ok {
		return
	}
	g.metrics().miss()
	g.execute(w, r, next, s, token)
}

// requestSlot derives the request's slot from its key, user and body. It
// writes the error and returns false when the request cannot have one.
func (g *guard) requestSlot(w http.ResponseWriter, r *http.Request, rawKey string) (slot, bool) {
	userID, ok := g.userID(w, r)
	if !ok {
		return slot{}, false
	}
	// FR-029 [HIGH]: include canonical query string and any
	// configured semantic headers in the fingerprint so two
	// requests that differ on query/header (e.g., dry_run=true vs
	// false) do not collide on the same body+key.
	key, err := fingerprintKey(r, rawKey, userID, g.cfg.semanticHeaders)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest,
			"configured semantic idempotency headers are required exactly once")
		return slot{}, false
	}
	s := slot{key: key, rawKey: rawKey}
	if g.cfg.fingerprintBody {
		s.fingerprint, ok = fingerprintRequestBody(w, r)
	}
	return s, ok
}

// userID returns the identifier that scopes the key, or "" for shared keys.
// It writes the error and returns false when the extractor yields no valid
// user.
func (g *guard) userID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if g.cfg.userExtractor == nil {
		return "", true
	}
	userID, ok := safeUserExtractor(g.cfg.logger, g.cfg.userExtractor, r)
	// Fail closed on an empty user: collapsing to (method, path, rawKey)
	// would let an anonymous request share a cache slot with another
	// anonymous (or extractor-failed) caller and replay the previous
	// response body via the same key.
	if !ok || userID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "idempotency requires authenticated request")
		return "", false
	}
	if err := idem.ValidateKey(userID); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "idempotency requires valid authenticated identity")
		return "", false
	}
	return userID, true
}

// fingerprintRequestBody fingerprints the request body and puts the buffered
// body back so the downstream handler can still read it. It writes the error
// and returns false when the body cannot be read. A read failure is a
// client or transport problem (oversized payload, disconnect, truncated
// upload), not a store error, so it never bumps the store-errors counter.
func fingerprintRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	fp, body, err := readAndFingerprintBody(r)
	var maxBytesErr *http.MaxBytesError
	switch {
	case err == nil:
		r.Body = io.NopCloser(bytes.NewReader(body))
		return fp, true
	case errors.Is(err, errBodyTooLarge):
		httpx.WriteError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("request body exceeds idempotency fingerprint limit (%d bytes)", maxFingerprintBodySize))
	case errors.Is(err, errInvalidFingerprintHeader):
		httpx.WriteError(w, http.StatusBadRequest, "idempotency fingerprint headers are invalid")
	case errors.As(err, &maxBytesErr):
		// An upstream maxbody (http.MaxBytesReader) cap below the
		// fingerprint limit tripped while buffering the body.
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "request body exceeds configured maximum size")
	default:
		httpx.WriteError(w, http.StatusBadRequest, "could not read request body")
	}
	return nil, false
}

// replayed answers the request from the store when its slot holds a
// response or rejects it when the slot was taken by a different body. It
// reports whether the request is done; failure is the log message of a
// store error.
func (g *guard) replayed(w http.ResponseWriter, r *http.Request, s slot, failure string) bool {
	cached, fpMismatch, err := g.store.Get(r.Context(), s.key, s.fingerprint)
	switch {
	case err != nil:
		g.storeFailed(w, failure, err, s)
	case fpMismatch:
		g.rejectReuse(w)
	case cached != nil:
		g.metrics().hit()
		replay(w, cached, g.cfg.replayHeader)
	default:
		return false
	}
	return true
}

// lock takes the slot for this request. It writes the response and returns
// false when the slot cannot be taken.
func (g *guard) lock(w http.ResponseWriter, r *http.Request, s slot) (string, bool) {
	token, fpMismatch, locked, err := g.store.TryLock(r.Context(), s.key, s.fingerprint, g.cfg.lockOrCacheTTL())
	switch {
	case err != nil:
		g.storeFailed(w, "idempotency: store TryLock failed", err, s)
		return "", false
	case fpMismatch:
		g.rejectReuse(w)
		return "", false
	case locked:
		return token, true
	}
	// A concurrent request may have completed between our Get miss
	// and TryLock, leaving a cached response now. Re-Get once so
	// we replay the success instead of returning a spurious 409.
	if g.replayed(w, r, s, "idempotency: store Get after contended TryLock failed") {
		return "", false
	}
	g.metrics().conflict()
	httpx.WriteError(w, http.StatusConflict, "request already in progress")
	return "", false
}

func (g *guard) storeFailed(w http.ResponseWriter, msg string, err error, s slot) {
	g.cfg.logger.Error(msg, redact.Error(err), redact.String("key", s.rawKey))
	g.metrics().storeError()
	httpx.WriteError(w, http.StatusInternalServerError, "idempotency store error")
}

func (g *guard) rejectReuse(w http.ResponseWriter) {
	g.metrics().conflict()
	httpx.WriteError(w, http.StatusUnprocessableEntity,
		"idempotency key reused with a different request body")
}

// execute runs the handler on the locked slot and caches its response, or
// releases the slot when the response must not be cached.
func (g *guard) execute(w http.ResponseWriter, r *http.Request, next http.Handler, s slot, token string) {
	rec := &responseCapture{
		ResponseWriter:  w,
		capturedHeaders: make(http.Header),
		statusCode:      http.StatusOK,
		body:            &bytes.Buffer{},
	}

	panicked := true
	defer func() {
		if panicked {
			g.unlock(r, s, token, "panic")
		}
	}()

	next.ServeHTTP(rec, r)
	panicked = false

	// A handler that set headers via w.Header() but returned without
	// calling Write/WriteHeader produces an implicit 200. Captured headers
	// reach the real writer only inside WriteHeader, so flush them here or
	// the first caller would receive a bare 200 while the cached response
	// carries headers the original never emitted. Skip when the connection
	// was hijacked — WriteHeader on a hijacked conn only produces a
	// spurious net/http error log.
	if !rec.wroteHeader && !rec.hijacked {
		rec.WriteHeader(rec.statusCode)
	}
	switch {
	case rec.hijacked:
		g.unlock(r, s, token, "hijack")
	case rec.bodyOverflow:
		g.cfg.logger.Warn("idempotency: response too large to cache, skipping",
			redact.String("key", s.rawKey))
		g.unlock(r, s, token, "overflow")
	case g.cfg.uncachedStatuses[rec.statusCode]:
		// The caller opted this status out of caching (typically
		// transient 5xx). Release the lock instead of storing the
		// response so a retry with the same key re-runs the handler
		// and can recover, rather than replaying the error for the
		// full cache TTL.
		g.unlock(r, s, token, "uncached status")
	default:
		g.cache(r, s, token, rec)
	}
}

// unlock releases the slot; after names the reason in the log of a failure.
func (g *guard) unlock(r *http.Request, s slot, token, after string) {
	ctx, cancel := postHandlerContext(r.Context(), g.cfg.postHandlerTimeout)
	defer cancel()
	if err := g.store.Unlock(ctx, s.key, token); err != nil {
		g.cfg.logger.Error("idempotency: failed to unlock after "+after,
			redact.Error(err), redact.String("key", s.rawKey))
	}
}

// cache stores the captured response in the slot. On success Set replaces
// the lock with the response, so no separate Unlock is needed.
func (g *guard) cache(r *http.Request, s slot, token string, rec *responseCapture) {
	resp := idem.CachedResponse{
		StatusCode: rec.statusCode,
		Headers:    g.cacheableHeaders(rec.Header()),
		Body:       append([]byte(nil), rec.body.Bytes()...),
	}
	ctx, cancel := postHandlerContext(r.Context(), g.cfg.postHandlerTimeout)
	defer cancel()
	err := g.store.Set(ctx, s.key, token, resp, g.cfg.ttl)
	switch {
	case err == nil:
	case errors.Is(err, idem.ErrLockLost):
		// TTL expired and another caller has taken the slot —
		// don't fight them. Their response will be the one
		// future requests replay.
		g.cfg.logger.Warn("idempotency: lock lost before Set; another caller now owns the slot",
			redact.String("key", s.rawKey))
	default:
		g.metrics().storeError()
		// Keep the lock: it prevents duplicate execution during the
		// TTL window and expires naturally.
		g.cfg.logger.Error("idempotency: failed to cache response, lock held until TTL expiry",
			redact.Error(err), redact.String("key", s.rawKey))
	}
}

// cacheableHeaders copies the response headers without the identity-bearing
// ones the caller did not preserve.
func (g *guard) cacheableHeaders(h http.Header) map[string][]string {
	headers := make(map[string][]string, len(h))
	for k, vals := range h {
		// Canonicalise the key for BOTH the preserve override and the
		// strip list. preserveHeaders keys are stored canonical, so a
		// handler that wrote an identity header via direct map access
		// (rc.Header()["set-cookie"] = ...) would otherwise miss the
		// override and be stripped even when WithPreserveHeaders named it.
		canonical := http.CanonicalHeaderKey(k)
		if !g.cfg.preserveHeaders[canonical] && identityResponseHeaders[canonical] {
			continue
		}
		cp := make([]string, len(vals))
		copy(cp, vals)
		headers[k] = cp
	}
	return headers
}

func (g *guard) metrics() *Metrics { return g.cfg.metrics }

// The counters are nil-safe so the middleware runs without [WithMetrics].
func (m *Metrics) hit() {
	if m != nil {
		m.hits.Inc()
	}
}

func (m *Metrics) miss() {
	if m != nil {
		m.misses.Inc()
	}
}

func (m *Metrics) conflict() {
	if m != nil {
		m.conflicts.Inc()
	}
}

func (m *Metrics) storeError() {
	if m != nil {
		m.errors.Inc()
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
//
// url.Values.Encode already sorts keys (and preserves per-key value
// order), so it is exactly the canonical form we need — there is no
// need to pre-sort into a second url.Values.
func canonicalQuery(v url.Values) string {
	return v.Encode()
}

func replay(w http.ResponseWriter, cached *idem.CachedResponse, replayHeader string) {
	if replayHeader != "" {
		w.Header().Set(replayHeader, "true")
	}
	for k, vals := range cached.Headers {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(cached.StatusCode)
	_, _ = w.Write(cached.Body)
}

const maxCapturedBodySize = 1 << 20 // 1 MiB
