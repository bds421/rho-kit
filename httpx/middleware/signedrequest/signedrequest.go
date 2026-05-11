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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"

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
// Callers typically read from a config/secret store. Returns an
// error to reject unknown key IDs.
type KeyResolver func(keyID string) ([]byte, error)

// NonceStore is the abstraction over the replay-protection cache.
// Implementations must:
//   - Store the nonce with a TTL >= 2 * max clock skew.
//   - Return (true, nil) on first observation; (false, nil) on replay.
//   - Use a constant-time backend appropriate for the deployment shape
//     (in-process for single instance, Redis for multi-instance).
type NonceStore interface {
	SeenOrStore(nonce string) (firstTime bool, err error)
}

// Option configures the [Middleware].
type Option func(*config)

type config struct {
	resolver        KeyResolver
	nonceStore      NonceStore
	maxClockSkew    time.Duration
	requiredHeaders []string
	bodyMaxSize     int64
	now             func() time.Time
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

// WithClock overrides the time source for tests.
//
// Panics if now is nil — a nil clock would compile but blow up on the
// first signed request, well after construction.
func WithClock(now func() time.Time) Option {
	if now == nil {
		panic("signedrequest: WithClock requires a non-nil clock function")
	}
	return func(c *config) { c.now = now }
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
		resolver:     resolver,
		nonceStore:   nonceStore,
		maxClockSkew: 5 * time.Minute,
		bodyMaxSize:  10 * 1024 * 1024,
		now:          time.Now,
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
				writeError(w, err)
				return
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
	if !timestampWithinSkew(tsUnix, cfg.now(), cfg.maxClockSkew) {
		return ErrTimestampInvalid
	}

	gotMAC, err := decodeSignatureMAC(sig)
	if err != nil {
		return ErrSignatureInvalid
	}

	body, err := readBody(r, cfg.bodyMaxSize)
	if err != nil {
		return err
	}

	secret, err := cfg.resolver(keyID)
	if err != nil || len(secret) == 0 {
		return ErrSignatureInvalid
	}
	if len(secret) < minSecretLen {
		return ErrSecretTooShort
	}

	canonical := buildCanonical(r, ts, nonce, body, cfg.requiredHeaders)
	expected := hmacSHA256(secret, canonical)
	if !hmac.Equal(gotMAC, expected) {
		return ErrSignatureInvalid
	}

	first, err := cfg.nonceStore.SeenOrStore(nonce)
	if err != nil {
		return fmt.Errorf("signedrequest: nonce store: %w", err)
	}
	if !first {
		return ErrNonceReplayed
	}
	return nil
}

func timestampWithinSkew(tsUnix int64, now time.Time, skew time.Duration) bool {
	ts := time.Unix(tsUnix, 0)
	if ts.After(now) {
		return ts.Sub(now) <= skew
	}
	return now.Sub(ts) <= skew
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
	default:
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
	}
}

// readBody reads the request body up to max bytes and rewinds it so
// the downstream handler still sees the data. Returns ErrBodyTooLarge
// when the limit is exceeded.
func readBody(r *http.Request, max int64) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	originalBody := r.Body
	limited := io.LimitReader(originalBody, max+1)
	buf, err := io.ReadAll(limited)
	closeErr := originalBody.Close()
	if err != nil {
		return nil, safeWrap("signedrequest: read body failed", err)
	}
	if closeErr != nil {
		return nil, safeWrap("signedrequest: close body failed", closeErr)
	}
	if int64(len(buf)) > max {
		return nil, ErrBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, nil
}

// buildCanonical returns the deterministic string MAC'd by both
// signer and verifier. Format documented at the package level.
func buildCanonical(r *http.Request, ts, nonce string, body []byte, requiredHeaders []string) []byte {
	bodyHash := sha256.Sum256(body)
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

// SignCanonical computes the kit-format signature for outbound calls.
// Exported so the client-side wrapper (httpx/sign) can use the same
// canonical string as the verifier.
func SignCanonical(secret []byte, r *http.Request, ts, nonce string, body []byte, requiredHeaders []string) (string, error) {
	if len(secret) < minSecretLen {
		return "", ErrSecretTooShort
	}
	if err := validateCanonicalRequest(r); err != nil {
		return "", err
	}
	if ts == "" {
		return "", fmt.Errorf("%w: missing timestamp", ErrMissingHeaders)
	}
	if err := validateStrictHeaderValue(ts, timestampMaxLen); err != nil {
		return "", err
	}
	if !validNonce(nonce) {
		return "", ErrNonceInvalid
	}
	headers, err := normalizeHeaders(requiredHeaders)
	if err != nil {
		return "", err
	}
	for _, h := range headers {
		if err := validateRequiredHeaderValue(r, h); err != nil {
			return "", err
		}
	}
	if err := validateOptionalHeaderValue(r, canonicalContentTypeHeader); err != nil {
		return "", err
	}
	mac := hmacSHA256(secret, buildCanonical(r, ts, nonce, body, headers))
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
