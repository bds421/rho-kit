package idempotency

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"
)

type responseCapture struct {
	http.ResponseWriter
	capturedHeaders http.Header
	statusCode      int
	body            *bytes.Buffer
	wroteHeader     bool
	bodyOverflow    bool
	hijacked        bool
}

func (rc *responseCapture) Header() http.Header {
	return rc.capturedHeaders
}

func (rc *responseCapture) WriteHeader(code int) {
	// 1xx are informational, repeatable responses (e.g. 103 Early Hints):
	// net/http allows emitting them before the single final WriteHeader.
	// Forward them to the wire but do NOT latch — otherwise the first 1xx
	// would lock statusCode and suppress the real final status, and every
	// replay would serve the 1xx code with a body as the final response.
	if code >= 100 && code < 200 {
		// Sync staged headers so 103 Early Hints (Link preload, etc.)
		// actually reaches the client. Header() returns the private
		// capture map; without this copy the interim response is bare.
		for k, vals := range rc.capturedHeaders {
			rc.ResponseWriter.Header()[k] = vals
		}
		rc.ResponseWriter.WriteHeader(code)
		return
	}
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
	c, brw, err := h.Hijack()
	if err == nil {
		rc.hijacked = true
		// Suppress caching of whatever bytes we already captured — the
		// caller has taken control of the connection.
		rc.bodyOverflow = true
	}
	return c, brw, err
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
		panic("middleware/idempotency: WithUserExtractor requires a non-nil extractor")
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
			panic("middleware/idempotency: WithSemanticHeaders requires a valid HTTP header field name")
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
			panic("middleware/idempotency: WithPreserveHeaders requires a valid HTTP header field name")
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
