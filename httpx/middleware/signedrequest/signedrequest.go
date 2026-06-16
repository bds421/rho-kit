// Package signedrequest implements HMAC-signed HTTP request verification.
//
// Use this for webhook receivers, server-to-server APIs, and any
// machine-to-machine HTTP that crosses a trust boundary without mTLS.
//
// The wire format is:
//
//	X-Signature-Timestamp: <unix seconds>
//	X-Signature-Nonce:     <base64 16 random bytes>
//	X-Signature-Key-Id:    <which key signed this; opaque>
//	X-Signature:           hmac-sha256=<base64 mac>
//
// The MAC is computed over a deterministic canonical string composed
// of method, path, host, Content-Type, timestamp, nonce, sha256(body),
// and any extra headers the operator pinned via [WithRequiredHeaders].
//
// Replay protection requires a [NonceStore]. The middleware refuses
// to start without one: replay-vulnerable signing is worse than no
// signing because operators assume protection that isn't there.
//
// asvs: V13.1.1, V13.2.3, V11.1.2
package signedrequest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/core/v2/clock"
	"github.com/bds421/rho-kit/httpx/v2"
)

// Header names. Exported so client-side packages can use the same constants.
const (
	HeaderTimestamp = "X-Signature-Timestamp"
	HeaderNonce     = "X-Signature-Nonce"
	HeaderKeyID     = "X-Signature-Key-Id"
	HeaderSignature = "X-Signature"

	signaturePrefix            = "hmac-sha256="
	canonicalContentTypeHeader = "Content-Type"

	signatureMaxLen = len(signaturePrefix) + 44 // base64.StdEncoding.EncodedLen(sha256.Size)
	timestampMaxLen = 20                        // max int64 decimal length
	keyIDMaxLen     = 256
)

// minSecretLen matches HMAC-SHA256 output size and the floor enforced by
// crypto/signing. Sub-32-byte secrets resolved from the operator's secret
// store are rejected so a misconfigured deployment fails closed.
const minSecretLen = 32

var fallbackMAC [sha256.Size]byte

type safeCauseError struct {
	msg   string
	cause error
}

func (e safeCauseError) Error() string {
	return e.msg
}

func (e safeCauseError) Unwrap() error {
	return e.cause
}

func safeWrap(msg string, cause error) error {
	if cause == nil {
		return errors.New(msg)
	}
	return safeCauseError{msg: msg, cause: cause}
}

// newNonceStoreError wraps a [NonceStore] backend failure so it matches
// both errors.Is(_, ErrNonceStore) (for classification and the 500
// mapping) and errors.Is(_, cause) (so server-side logging can surface
// the underlying outage). The dual %w verbs produce a joined error
// whose Unwrap() []error reaches both targets.
func newNonceStoreError(cause error) error {
	if cause == nil {
		return ErrNonceStore
	}
	return fmt.Errorf("%w: %w", ErrNonceStore, cause)
}

// Sentinel errors. Verify is internal; the middleware translates these
// into 400/401/413 responses.
var (
	ErrMissingHeaders   = errors.New("signedrequest: missing one or more required headers")
	ErrTimestampInvalid = errors.New("signedrequest: timestamp invalid or out of skew")
	ErrSignatureInvalid = errors.New("signedrequest: signature did not verify")
	ErrNonceReplayed    = errors.New("signedrequest: nonce replayed")
	ErrNonceInvalid     = errors.New("signedrequest: nonce malformed or wrong length")
	ErrBodyTooLarge     = errors.New("signedrequest: body exceeds maximum")
	ErrSecretTooShort   = errors.New("signedrequest: resolved secret is shorter than the 32-byte HMAC-SHA256 minimum")
	ErrInvalidRequest   = errors.New("signedrequest: invalid request")
	// ErrNonceStore marks a failure of the [NonceStore] backend itself
	// (e.g. a Redis outage) rather than any client-attributable problem.
	// The middleware maps it to 500 and counts it under the store_error
	// metric reason so an infrastructure outage is not misreported as a
	// forged-signature attack spike. The underlying cause is wrapped and
	// reachable via errors.Unwrap / errors.Is.
	ErrNonceStore = errors.New("signedrequest: nonce store backend error")
)

// nonceMaxLen caps the wire-level nonce header. The kit's signing
// transport produces base64-RawURL of 16 random bytes (22 chars); a
// few extra characters of slack tolerate legitimate cross-runtime
// encodings while still preventing pathological key sizes downstream
// (audit FR-026).
const nonceMaxLen = 64

// validNonce checks the wire-format constraint: 16 random bytes
// rendered in any of the standard base64 alphabets (StdEncoding with
// padding, RawStdEncoding, URLEncoding with padding, RawURLEncoding).
// The kit's own signer uses StdEncoding with padding (24 chars); we
// accept the URL-safe variants too so callers in browser/JWT
// pipelines do not need a separate transport.
//
// Audit FR-026: bounding length and demanding a canonical decode
// prevents an attacker from inflating nonce-store keys with arbitrary
// strings or smuggling unprintable bytes via Redis.
func validNonce(nonce string) bool {
	if len(nonce) == 0 || len(nonce) > nonceMaxLen {
		return false
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if decoded, err := enc.DecodeString(nonce); err == nil && len(decoded) == 16 {
			return true
		}
	}
	return false
}

// KeyResolver returns the HMAC secret bytes for the given key ID.
// Callers typically read from a config / secret manager / Vault /
// KMS adapter. The ctx is the inbound request context, so the
// resolver can honour the caller's deadline and cancellation when
// talking to a remote secret store. Return an error to reject
// unknown key IDs; the middleware maps a non-nil err or empty
// secret to [ErrSignatureInvalid] without leaking the underlying
// reason to the client.
type KeyResolver func(ctx context.Context, keyID string) ([]byte, error)

// NonceStore is the abstraction over the replay-protection cache.
// Implementations must:
//   - Store the nonce with a TTL >= 2 * max clock skew.
//   - Return (true, nil) on first observation; (false, nil) on replay.
//   - Use a constant-time backend appropriate for the deployment shape
//     (in-process for single instance, Redis for multi-instance).
type NonceStore interface {
	// SeenOrStore observes a nonce within the caller's context. The ctx
	// is the inbound HTTP request context (with the store's own per-call
	// timeout applied by the implementation), so a cancelled request
	// releases the backend connection promptly instead of pinning it
	// against a detached background ctx.
	SeenOrStore(ctx context.Context, nonce string) (firstTime bool, err error)
}

// Option configures the [Middleware].
type Option func(*config)

type config struct {
	resolver        KeyResolver
	nonceStore      NonceStore
	maxClockSkew    time.Duration
	requiredHeaders []string
	bodyMaxSize     int64
	inMemoryBodyMax int64
	now             clock.Func
	metrics         *Metrics
	logger          *slog.Logger
}

// WithMaxClockSkew sets the tolerance window. Tokens with timestamps
// outside [now-skew, now+skew] are rejected. Default: 5 minutes.
//
// Panics if d is non-positive — a zero or negative skew window would
// reject every legitimate request and is almost certainly a wiring bug.
func WithMaxClockSkew(d time.Duration) Option {
	if d <= 0 {
		panic("signedrequest: WithMaxClockSkew requires a positive duration")
	}
	return func(c *config) { c.maxClockSkew = d }
}

// WithRequiredHeaders pins additional headers into the canonical
// signing string. Names are case-insensitive; values are taken
// verbatim from the request. The middleware rejects requests that
// omit any required header.
//
// Panics if any name is empty or fails RFC 7230 header-field-name
// validation — an invalid name would force every request to fail with
// a confusing missing-header error and almost certainly indicates a
// wiring bug.
func WithRequiredHeaders(names ...string) Option {
	canonical := make([]string, 0, len(names))
	for _, n := range names {
		if !httpguts.ValidHeaderFieldName(n) {
			panic("signedrequest: WithRequiredHeaders requires a valid HTTP header field name")
		}
		canonical = append(canonical, strings.ToLower(n))
	}
	return func(c *config) {
		c.requiredHeaders = append(c.requiredHeaders, canonical...)
	}
}

// WithBodyMaxSize bounds the request body that will be MAC'd. Bodies
// past the limit are rejected with 413. Default: 10 MiB.
//
// Panics if n is non-positive — a zero/negative cap would either reject
// every body or behave unpredictably depending on the LimitReader path.
func WithBodyMaxSize(n int64) Option {
	if n <= 0 {
		panic("signedrequest: WithBodyMaxSize requires a positive byte cap")
	}
	return func(c *config) { c.bodyMaxSize = n }
}

// WithInMemoryBodyMax caps how many request-body bytes stay in memory
// before the verifier spools the rest to a bounded temp file. The
// threshold bounds heap amplification per authenticated request: a
// caller with a valid key id who never produces a verifiable MAC can
// only pin n bytes of heap (default 64 KiB), not the full
// [WithBodyMaxSize] cap. Disk usage stays bounded by bodyMaxSize.
//
// Panics if n is non-positive.
func WithInMemoryBodyMax(n int64) Option {
	if n <= 0 {
		panic("signedrequest: WithInMemoryBodyMax requires a positive byte cap")
	}
	return func(c *config) { c.inMemoryBodyMax = n }
}

// WithClock overrides the time source for tests.
//
// Panics if now is nil — a nil clock would compile but blow up on the
// first signed request, well after construction.
func WithClock(now clock.Func) Option {
	if now == nil {
		panic("signedrequest: WithClock requires a non-nil clock function")
	}
	return func(c *config) { c.now = now }
}

// WithMetrics attaches a [Metrics] instance to the middleware so each
// verification failure increments the matching reason counter.
//
// Panics if m is nil — a silent no-op would defeat the purpose of an
// "observability enabled" toggle. Omit the option entirely to opt out.
func WithMetrics(m *Metrics) Option {
	if m == nil {
		panic("signedrequest: WithMetrics requires non-nil metrics (omit the option for no metrics)")
	}
	return func(c *config) { c.metrics = m }
}

// WithLogger sets the fallback *slog.Logger used to record server-side
// verification faults — nonce-store backend failures and operator
// misconfiguration — at error level. Client-attributable failures (bad
// signature, missing headers, replay, etc.) are intentionally NOT
// logged so an attacker cannot flood the operator's logs.
//
// When a request-scoped logger is present in the request context (e.g.
// installed by the httpx request-logging middleware), that logger takes
// precedence over the one set here. When neither is set the package
// falls back to [slog.Default].
//
// Panics if l is nil — a silent no-op would defeat the purpose of the
// option. Omit it entirely to rely on the context logger / slog.Default.
func WithLogger(l *slog.Logger) Option {
	if l == nil {
		panic("signedrequest: WithLogger requires a non-nil logger (omit the option to use the context logger or slog.Default)")
	}
	return func(c *config) { c.logger = l }
}

// Middleware constructs the verification middleware.
//
// resolver is called once per request to obtain the secret keyed by
// the X-Signature-Key-Id header. nonceStore is the replay-protection
// store; the constructor panics if nil to fail loudly at startup.
func Middleware(resolver KeyResolver, nonceStore NonceStore, opts ...Option) func(http.Handler) http.Handler {
	if resolver == nil {
		panic("signedrequest: KeyResolver must not be nil")
	}
	if nonceStore == nil {
		panic("signedrequest: NonceStore must not be nil — replay-vulnerable signing is worse than no signing")
	}

	cfg := config{
		resolver:        resolver,
		nonceStore:      nonceStore,
		maxClockSkew:    5 * time.Minute,
		bodyMaxSize:     10 * 1024 * 1024,
		inMemoryBodyMax: defaultInMemoryBodyMax,
		now:             time.Now,
	}
	for _, o := range opts {
		if o == nil {
			panic("signedrequest: Middleware option must not be nil")
		}
		o(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := verify(r, &cfg); err != nil {
				cfg.metrics.observeVerifyFailure(classifyVerifyFailure(err))
				cfg.logVerifyError(r.Context(), err)
				writeError(w, err)
				return
			}
			// verify() may swap r.Body for a spooled reader backed by a
			// temp file. net/http closes only the ORIGINAL body it
			// captured before the handler ran (server.response.reqBody),
			// not the one we install here — so a downstream handler that
			// never reads or closes r.Body would leak the spooled
			// *os.File until the GC finalizer runs (fd exhaustion on
			// Unix, orphaned temp files on Windows). Close the installed
			// body ourselves once the handler returns; spooledReader.Close
			// is idempotent, so a handler that already closed it is unharmed.
			installed := r.Body
			if installed != nil {
				defer func() { _ = installed.Close() }()
			}
			next.ServeHTTP(w, r)
		})
	}
}

func verify(r *http.Request, cfg *config) error {
	ts, err := requiredSingletonHeaderBounded(r, HeaderTimestamp, timestampMaxLen)
	if err != nil {
		return err
	}
	nonce, err := requiredSingletonHeaderBounded(r, HeaderNonce, nonceMaxLen)
	if err != nil {
		return err
	}
	keyID, err := requiredSingletonHeaderBounded(r, HeaderKeyID, keyIDMaxLen)
	if err != nil {
		return err
	}
	sig, err := requiredSingletonHeaderBounded(r, HeaderSignature, signatureMaxLen)
	if err != nil {
		return err
	}
	// FR-026 [MED]: validate nonce format/length before it can become
	// a Redis key. The wire contract is "16 random bytes,
	// base64-RawURL-encoded" which is exactly 22 ASCII characters.
	// Capping length and demanding the canonical encoding prevents an
	// attacker from inflating nonce-store keys to pathological sizes
	// or smuggling unprintable bytes into Redis.
	if !validNonce(nonce) {
		return ErrNonceInvalid
	}
	for _, h := range cfg.requiredHeaders {
		if err := validateRequiredHeaderValue(r, h); err != nil {
			return err
		}
	}
	if err := validateOptionalHeaderValue(r, canonicalContentTypeHeader); err != nil {
		return err
	}
	if err := validateCanonicalRequest(r); err != nil {
		return err
	}

	tsUnix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrTimestampInvalid
	}
	if direction := classifyTimestampSkew(tsUnix, cfg.now(), cfg.maxClockSkew); direction != 0 {
		// Both branches still satisfy errors.Is(_, ErrTimestampInvalid),
		// so writeError keeps returning 400 unchanged; the wrappers
		// exist so the metrics layer can split "future-skew" from
		// "past-maxAge" without re-deriving the direction.
		if direction > 0 {
			return fmt.Errorf("%w: %w", ErrTimestampInvalid, errTimestampClockSkew)
		}
		return fmt.Errorf("%w: %w", ErrTimestampInvalid, errTimestampExpired)
	}

	gotMAC, err := decodeSignatureMAC(sig)
	if err != nil {
		return ErrSignatureInvalid
	}

	// Resolve the secret BEFORE buffering the body. Without this, an
	// unauthenticated caller can force the server to buffer up to
	// bodyMaxSize bytes per request — a memory-amplification primitive
	// against any endpoint that mounts this middleware.
	secret, err := cfg.resolver(r.Context(), keyID)
	if err != nil || len(secret) == 0 {
		return ErrSignatureInvalid
	}
	if len(secret) < minSecretLen {
		// Wipe the over-short secret before returning — the resolver
		// may have leaked the operator's actual key material into a
		// new []byte and we should not let it linger on the heap any
		// longer than necessary.
		for i := range secret {
			secret[i] = 0
		}
		return ErrSecretTooShort
	}
	// Ensure the per-request copy of the HMAC key is zeroed before
	// verify() returns — bounds the lifetime of plaintext key bytes
	// to the duration of a single MAC compute (Lens F A.7).
	defer func() {
		for i := range secret {
			secret[i] = 0
		}
	}()

	// Stream the body through a SHA-256 hasher. Up to inMemoryBodyMax
	// bytes are kept in memory; anything beyond spools to a private
	// temp file. This bounds heap amplification per authenticated
	// request: a caller with a valid key id who never produces a
	// verifiable MAC can only pin inMemoryBodyMax bytes of heap, not
	// the full bodyMaxSize cap. Disk usage stays bounded by
	// bodyMaxSize.
	spooled, bodyHash, err := readSpooledBody(r, cfg.bodyMaxSize, cfg.inMemoryBodyMax)
	if err != nil {
		return err
	}

	canonical := buildCanonicalFromHash(r, ts, nonce, bodyHash, cfg.requiredHeaders)
	expected := hmacSHA256(secret, canonical)
	if !hmac.Equal(gotMAC, expected) {
		// MAC mismatch: discard the spooled body (and the temp file,
		// if any) before returning. The downstream handler will not
		// run, so we own cleanup here.
		spooled.cleanup()
		return ErrSignatureInvalid
	}

	first, err := cfg.nonceStore.SeenOrStore(r.Context(), nonce)
	if err != nil {
		spooled.cleanup()
		return newNonceStoreError(err)
	}
	if !first {
		spooled.cleanup()
		return ErrNonceReplayed
	}

	// Only after MAC verifies AND nonce checks do we expose the body
	// to the downstream handler. The returned ReadCloser removes any
	// underlying temp file on Close. net/http does NOT close this body
	// (it only closes the original it captured before the handler ran),
	// so the Middleware wrapper closes it after the handler returns.
	r.Body = spooled.Body()
	return nil
}

// classifyTimestampSkew returns 0 when the timestamp is within
// [now-skew, now+skew]. A positive return means the timestamp is too
// far in the future (clock-skew); a negative return means it is too
// far in the past (expired). Splitting the direction lets metrics
// distinguish the two without changing the writeError behaviour.
func classifyTimestampSkew(tsUnix int64, now time.Time, skew time.Duration) int {
	ts := time.Unix(tsUnix, 0)
	if ts.After(now) {
		if ts.Sub(now) <= skew {
			return 0
		}
		return 1
	}
	if now.Sub(ts) <= skew {
		return 0
	}
	return -1
}

func decodeSignatureMAC(sig string) ([]byte, error) {
	if !strings.HasPrefix(sig, signaturePrefix) {
		return nil, ErrSignatureInvalid
	}
	gotMAC, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sig, signaturePrefix))
	if err != nil {
		return nil, ErrSignatureInvalid
	}
	if len(gotMAC) != sha256.Size {
		return fallbackMAC[:], nil
	}
	return gotMAC, nil
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBodyTooLarge):
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "request entity too large")
	case errors.Is(err, ErrMissingHeaders), errors.Is(err, ErrTimestampInvalid), errors.Is(err, ErrNonceInvalid), errors.Is(err, ErrInvalidRequest):
		httpx.WriteError(w, http.StatusBadRequest, "bad request")
	case errors.Is(err, ErrSignatureInvalid), errors.Is(err, ErrNonceReplayed):
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, ErrSecretTooShort):
		// Operator misconfiguration: the resolver returned a too-short key.
		// 500 keeps the failure mode visible without leaking which key ID
		// was tried.
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
	case errors.Is(err, ErrNonceStore):
		// Server-side dependency outage (e.g. Redis down). 500 keeps the
		// failure attributed to the server, not the client; the cause is
		// logged via logVerifyError without leaking it to the response.
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
	default:
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
	}
}

// logVerifyError records server-side verification faults at error level
// so an operator has a diagnostic for the 500 the client receives.
// Only server-attributable failures are logged: a nonce-store backend
// outage ([ErrNonceStore]), operator misconfiguration ([ErrSecretTooShort]),
// or an otherwise-unclassified internal error. Client-attributable
// failures (bad signature, missing/invalid headers, replay, oversized
// body) are deliberately skipped so an attacker cannot flood the logs.
//
// The request-scoped logger (if any) takes precedence over cfg.logger;
// [httpx.Logger] falls back to [slog.Default] when neither is set, so
// these faults are never silently dropped.
func (cfg *config) logVerifyError(ctx context.Context, err error) {
	if !isServerSideVerifyError(err) {
		return
	}
	httpx.Logger(ctx, cfg.logger).Error(
		"signedrequest: verification failed server-side",
		slog.String("error", err.Error()),
	)
}

// isServerSideVerifyError reports whether err represents a server-side
// fault that warrants an error-level log entry, as opposed to an
// expected client-attributable rejection.
func isServerSideVerifyError(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrNonceStore), errors.Is(err, ErrSecretTooShort):
		return true
	case errors.Is(err, ErrMissingHeaders),
		errors.Is(err, ErrTimestampInvalid),
		errors.Is(err, ErrSignatureInvalid),
		errors.Is(err, ErrNonceReplayed),
		errors.Is(err, ErrNonceInvalid),
		errors.Is(err, ErrBodyTooLarge),
		errors.Is(err, ErrInvalidRequest):
		return false
	default:
		// Unclassified errors map to 500 in writeError; surface them.
		return true
	}
}

// streamBody buffers the body in memory and returns its SHA-256 hash.
// Used only by the offline Sign helper, which operates on
// caller-controlled outbound bodies (no amplification risk). The
// server-side verifier uses [readSpooledBody] instead, which spools
// bodies that exceed inMemoryBodyMax to a private temp file so a
// single authenticated caller cannot pin bodyMaxSize bytes of heap.
// Returns ErrBodyTooLarge when the limit is exceeded.
func streamBody(r *http.Request, max int64) ([]byte, [32]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, sha256.Sum256(nil), nil
	}
	originalBody := r.Body
	limited := io.LimitReader(originalBody, max+1)
	hasher := sha256.New()
	var buf bytes.Buffer
	tee := io.TeeReader(limited, hasher)
	_, err := io.Copy(&buf, tee)
	closeErr := originalBody.Close()
	if err != nil {
		return nil, [32]byte{}, safeWrap("signedrequest: read body failed", err)
	}
	if closeErr != nil {
		return nil, [32]byte{}, safeWrap("signedrequest: close body failed", closeErr)
	}
	if int64(buf.Len()) > max {
		return nil, [32]byte{}, ErrBodyTooLarge
	}
	var sum [32]byte
	copy(sum[:], hasher.Sum(nil))
	return buf.Bytes(), sum, nil
}

// readBody buffers the body and rewinds r.Body for callers that need
// the bytes without a streaming hash. Kept for the offline Sign helper
// that produces canonical strings outside the verify path.
func readBody(r *http.Request, max int64) ([]byte, error) {
	body, _, err := streamBody(r, max)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// buildCanonical returns the deterministic string MAC'd by both
// signer and verifier. Format documented at the package level.
func buildCanonical(r *http.Request, ts, nonce string, body []byte, requiredHeaders []string) []byte {
	bodyHash := sha256.Sum256(body)
	return buildCanonicalFromHash(r, ts, nonce, bodyHash, requiredHeaders)
}

// buildCanonicalFromHash is the streaming-friendly variant used by
// verify() where bodyHash has already been computed while reading.
func buildCanonicalFromHash(r *http.Request, ts, nonce string, bodyHash [32]byte, requiredHeaders []string) []byte {
	host, _ := canonicalRequestHost(r)
	contentType, _ := optionalSingletonHeader(r, canonicalContentTypeHeader)
	parts := []string{
		r.Method,
		r.URL.RequestURI(),
		host,
		contentType,
		ts,
		nonce,
		hex.EncodeToString(bodyHash[:]),
	}
	if len(requiredHeaders) > 0 {
		hdrs := append([]string(nil), requiredHeaders...)
		sort.Strings(hdrs)
		for _, h := range hdrs {
			value, _ := requiredSingletonHeader(r, h)
			parts = append(parts, h+":"+value)
		}
	}
	return []byte(strings.Join(parts, "\n"))
}

func validateCanonicalRequest(r *http.Request) error {
	if r == nil {
		return fmt.Errorf("%w: nil request", ErrInvalidRequest)
	}
	if r.URL == nil {
		return fmt.Errorf("%w: nil URL", ErrInvalidRequest)
	}
	if !httpguts.ValidHeaderFieldName(r.Method) {
		return fmt.Errorf("%w: invalid method", ErrInvalidRequest)
	}
	if strings.ContainsAny(r.URL.RequestURI(), "\r\n") {
		return fmt.Errorf("%w: request URI contains CR/LF", ErrInvalidRequest)
	}
	if _, err := canonicalRequestHost(r); err != nil {
		return err
	}
	return nil
}

func canonicalRequestHost(r *http.Request) (string, error) {
	host := r.Host
	if host == "" && r.URL != nil {
		host = r.URL.Host
	}
	if host == "" {
		return "", fmt.Errorf("%w: empty host", ErrInvalidRequest)
	}
	if !httpguts.ValidHostHeader(host) {
		return "", fmt.Errorf("%w: invalid host", ErrInvalidRequest)
	}
	return strings.ToLower(host), nil
}

func validateRequiredHeaderValue(r *http.Request, name string) error {
	_, err := requiredSingletonHeader(r, name)
	return err
}

func validateOptionalHeaderValue(r *http.Request, name string) error {
	_, err := optionalSingletonHeader(r, name)
	return err
}

func optionalSingletonHeader(r *http.Request, name string) (string, error) {
	values := r.Header.Values(name)
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("%w: header has multiple values", ErrInvalidRequest)
	}
	if !httpguts.ValidHeaderFieldValue(values[0]) {
		return "", fmt.Errorf("%w: header contains invalid characters", ErrInvalidRequest)
	}
	return values[0], nil
}

func requiredSingletonHeader(r *http.Request, name string) (string, error) {
	return requiredSingletonHeaderBounded(r, name, 0)
}

func requiredSingletonHeaderBounded(r *http.Request, name string, maxLen int) (string, error) {
	values := r.Header.Values(name)
	if len(values) == 0 {
		return "", fmt.Errorf("%w: required header is missing", ErrMissingHeaders)
	}
	if len(values) != 1 {
		return "", fmt.Errorf("%w: required header has multiple values", ErrInvalidRequest)
	}
	value := values[0]
	if value == "" {
		return "", fmt.Errorf("%w: required header is missing or empty", ErrMissingHeaders)
	}
	if err := validateStrictHeaderValue(value, maxLen); err != nil {
		return "", err
	}
	return value, nil
}

func validateStrictHeaderValue(value string, maxLen int) error {
	if maxLen > 0 && len(value) > maxLen {
		return fmt.Errorf("%w: required header exceeds maximum size", ErrInvalidRequest)
	}
	if !httpguts.ValidHeaderFieldValue(value) {
		return fmt.Errorf("%w: required header contains invalid characters", ErrInvalidRequest)
	}
	if maxLen > 0 && strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: required header contains surrounding whitespace", ErrInvalidRequest)
	}
	if maxLen > 0 && strings.Contains(value, ",") {
		return fmt.Errorf("%w: required header contains an ambiguous comma", ErrInvalidRequest)
	}
	return nil
}

func hmacSHA256(secret, msg []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(msg)
	return h.Sum(nil)
}

// SignRequest carries the inputs to [SignCanonical]. Named fields
// keep the 6-input call site readable — Secret/Body are both byte
// slices and Timestamp/Nonce are both short strings, so the positional
// form was easy to misorder at the call site.
type SignRequest struct {
	// Secret is the shared HMAC key (>= 32 bytes — minSecretLen).
	Secret []byte
	// Request is the outbound *http.Request whose canonical form is
	// signed. Body is supplied separately via Body so the caller can
	// retain ownership of the reader (the canonical form does not
	// consume the request body).
	Request *http.Request
	// Timestamp is the [HeaderTimestamp] value (unix seconds, decimal).
	Timestamp string
	// Nonce is the [HeaderNonce] value: the base64 encoding (Std,
	// RawStd, URL, or RawURL alphabet) of exactly 16 random bytes.
	Nonce string
	// Body is the request body bytes that will be sent on the wire.
	// Pass nil for bodyless requests; pass the same bytes that the
	// recipient's middleware will receive.
	Body []byte
	// RequiredHeaders names the request headers that must be present
	// and covered by the signature; the verifier rejects requests that
	// drop any of them. Nil is "the canonical defaults".
	RequiredHeaders []string
}

// SignCanonical computes the kit-format signature for outbound calls.
// Exported so the client-side wrapper (httpx/sign) can use the same
// canonical string as the verifier. See [SignRequest] for the field
// contract.
func SignCanonical(sr SignRequest) (string, error) {
	if len(sr.Secret) < minSecretLen {
		return "", ErrSecretTooShort
	}
	if err := validateCanonicalRequest(sr.Request); err != nil {
		return "", err
	}
	if sr.Timestamp == "" {
		return "", fmt.Errorf("%w: missing timestamp", ErrMissingHeaders)
	}
	if err := validateStrictHeaderValue(sr.Timestamp, timestampMaxLen); err != nil {
		return "", err
	}
	if !validNonce(sr.Nonce) {
		return "", ErrNonceInvalid
	}
	headers, err := normalizeHeaders(sr.RequiredHeaders)
	if err != nil {
		return "", err
	}
	for _, h := range headers {
		if err := validateRequiredHeaderValue(sr.Request, h); err != nil {
			return "", err
		}
	}
	if err := validateOptionalHeaderValue(sr.Request, canonicalContentTypeHeader); err != nil {
		return "", err
	}
	mac := hmacSHA256(sr.Secret, buildCanonical(sr.Request, sr.Timestamp, sr.Nonce, sr.Body, headers))
	return signaturePrefix + base64.StdEncoding.EncodeToString(mac), nil
}

func normalizeHeaders(hs []string) ([]string, error) {
	out := make([]string, len(hs))
	for i, h := range hs {
		if !httpguts.ValidHeaderFieldName(h) {
			return nil, fmt.Errorf("%w: invalid required header", ErrInvalidRequest)
		}
		out[i] = strings.ToLower(h)
	}
	return out, nil
}
