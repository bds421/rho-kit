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

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.requiredMethods[r.Method] {
				next.ServeHTTP(w, r)
				return
			}

			rawKey, ok := singleHeaderValue(r.Header, cfg.header)
			if !ok {
				if cfg.optionalKey && len(r.Header.Values(cfg.header)) == 0 {
					next.ServeHTTP(w, r)
					return
				}
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
					var maxBytesErr *http.MaxBytesError
					if errors.As(fpErr, &maxBytesErr) {
						// An upstream maxbody (http.MaxBytesReader) cap below the
						// fingerprint limit tripped while buffering the body. This
						// is a client/oversized-payload failure, not a store error:
						// surface 413 and do NOT bump the store-errors counter.
						httpx.WriteError(w, http.StatusRequestEntityTooLarge,
							"request body exceeds configured maximum size")
						return
					}
					// A read failure here is a client/transport problem (client
					// disconnect, truncated upload), not a store error — do not
					// bump the store-errors counter for it.
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
				cfg.logger.Error("idempotency: store Get failed",
					redact.Error(err), redact.String("key", rawKey))
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
				replay(w, cached, cfg.replayHeader)
				return
			}

			token, fpMismatchOnLock, locked, lockErr := store.TryLock(r.Context(), key, bodyFingerprint, cfg.lockOrCacheTTL())
			if lockErr != nil {
				cfg.logger.Error("idempotency: store TryLock failed",
					redact.Error(lockErr), redact.String("key", rawKey))
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
				// A concurrent request may have completed between our Get miss
				// and TryLock, leaving a cached response now. Re-Get once so
				// we replay the success instead of returning a spurious 409.
				cached, fpMismatch2, getErr := store.Get(r.Context(), key, bodyFingerprint)
				if getErr != nil {
					cfg.logger.Error("idempotency: store Get after contended TryLock failed",
						redact.Error(getErr), redact.String("key", rawKey))
					if cfg.metrics != nil {
						cfg.metrics.errors.Inc()
					}
					httpx.WriteError(w, http.StatusInternalServerError, "idempotency store error")
					return
				}
				if fpMismatch2 {
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
					replay(w, cached, cfg.replayHeader)
					return
				}
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

			// A handler that set headers via w.Header() but returned without
			// calling Write/WriteHeader produces an implicit 200. Until now
			// captured headers were only copied to the real writer inside
			// WriteHeader, so the first caller received a bare 200 while the
			// cache snapshot below recorded the unsent headers — every replay
			// then carried headers the original response never emitted. Flush
			// the captured headers to the underlying writer so the first
			// caller and the cached/replayed response agree. Skip when the
			// connection was hijacked — WriteHeader on a hijacked conn only
			// produces a spurious net/http error log.
			if !rec.wroteHeader && !rec.hijacked {
				rec.WriteHeader(rec.statusCode)
			}
			if rec.hijacked {
				ctx, cancel := postHandlerContext(r.Context(), cfg.postHandlerTimeout)
				defer cancel()
				if unlockErr := store.Unlock(ctx, key, token); unlockErr != nil {
					cfg.logger.Error("idempotency: failed to unlock after hijack",
						redact.Error(unlockErr), redact.String("key", rawKey))
				}
				return
			}

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

			if cfg.uncachedStatuses[rec.statusCode] {
				// The caller opted this status out of caching (typically
				// transient 5xx). Release the lock instead of storing the
				// response so a retry with the same key re-runs the handler
				// and can recover, rather than replaying the error for the
				// full cache TTL.
				ctx, cancel := postHandlerContext(r.Context(), cfg.postHandlerTimeout)
				defer cancel()
				if unlockErr := store.Unlock(ctx, key, token); unlockErr != nil {
					cfg.logger.Error("idempotency: failed to unlock after uncached status",
						redact.Error(unlockErr), redact.String("key", rawKey))
				}
				return
			}

			headers := make(map[string][]string, len(rec.Header()))
			for k, vals := range rec.Header() {
				// Canonicalise the key for BOTH the preserve override and the
				// strip list. preserveHeaders keys are stored canonical, so a
				// handler that wrote an identity header via direct map access
				// (rc.Header()["set-cookie"] = ...) would otherwise miss the
				// override and be stripped even when WithPreserveHeaders named it.
				canonical := http.CanonicalHeaderKey(k)
				if !cfg.preserveHeaders[canonical] && identityResponseHeaders[canonical] {
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
					if cfg.metrics != nil {
						cfg.metrics.errors.Inc()
					}
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
