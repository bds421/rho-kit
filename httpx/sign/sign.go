// Package sign provides an http.RoundTripper that signs every outbound
// request using the wire format expected by
// httpx/middleware/signedrequest.
//
//	base := httpx.NewHTTPClient(10*time.Second, tlsConfig)
//	client := &http.Client{
//	    Transport: sign.Wrap(base.Transport, secret, "prod-2026"),
//	    Timeout:   base.Timeout,
//	}
//	resp, err := client.Post(url, "application/json", body)
//
// The wrapper reads the request body, computes a SHA-256 hash, signs
// the canonical string, and rewinds the body before dispatching. Body
// reading is bounded by [WithBodyMaxSize] (default 10 MiB).
package sign

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/core/v2/clock"
	"github.com/bds421/rho-kit/httpx/v2/internal/transportdefaults"
	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
)

// minSecretLen matches HMAC-SHA256 output size and the floor enforced by
// crypto/signing. Sub-32-byte secrets do not provide the security HMAC-SHA256
// is designed for and are rejected at construction.
const minSecretLen = 32

// keyIDMaxLen mirrors the verifier-side cap for X-Signature-Key-Id.
const keyIDMaxLen = 256

// ErrInvalidRequest is returned when the signing transport is asked
// to sign a structurally invalid HTTP request.
var ErrInvalidRequest = errors.New("sign: invalid request")

// KeyStore supplies the active outbound signing key. Implementations
// must be safe for concurrent use. Returning a different key ID /
// secret pair over time lets clients rotate HMAC credentials without
// rebuilding the HTTP client.
//
// The ctx is the per-request context — KMS / Vault / Secrets Manager
// adapters should honour its deadline and cancellation when fetching
// or refreshing the active key. Return:
//
//   - the current keyID + secret + nil on success.
//   - a non-nil error on provider failure (typically wrapping
//     [ErrKeyStoreUnavailable]). The transport reports the error to
//     the caller; the inbound HTTP request is aborted before the
//     network roundtrip starts. Static in-memory stores never need
//     to surface an error.
type KeyStore interface {
	CurrentKeyID(ctx context.Context) (keyID string, secret []byte, err error)
}

// ErrKeyStoreUnavailable is the typed sentinel for transient
// KeyStore failures (KMS / Vault / Secrets Manager outage,
// rate-limit, network error). Distinct from a misconfigured store
// (rejected at construction) so callers can retry the signed request
// on this error rather than treat it as permanent.
var ErrKeyStoreUnavailable = errors.New("sign: key store unavailable")

// Option configures the [Wrap] RoundTripper.
type Option func(*config)

type config struct {
	includeHeaders []string
	bodyMaxSize    int64
	now            clock.Func
	nonceFn        func() (string, error)
}

var nonceRandReader io.Reader = rand.Reader

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

// WithIncludeHeaders pins additional headers into the canonical
// signing string. The names are also passed to the verifier via the
// matching WithRequiredHeaders option — the two MUST agree, otherwise
// every request fails verification.
//
// Panics on an invalid HTTP header field name (audit FR-028) — the
// verifier rejects invalid names at construction, so accepting them
// here would silently produce a signature profile no production
// verifier accepts.
func WithIncludeHeaders(names ...string) Option {
	canonical := make([]string, 0, len(names))
	for _, n := range names {
		if !httpguts.ValidHeaderFieldName(n) {
			panic("sign: WithIncludeHeaders requires a valid HTTP header field name")
		}
		canonical = append(canonical, strings.ToLower(n))
	}
	return func(c *config) {
		c.includeHeaders = append(c.includeHeaders, canonical...)
	}
}

// WithBodyMaxSize bounds the size of the request body the wrapper
// will buffer to compute the signature. Default: 10 MiB.
//
// Panics if n is non-positive — options are wired at startup and a
// zero/negative cap silently breaks every signed request.
func WithBodyMaxSize(n int64) Option {
	if n <= 0 {
		panic("sign: WithBodyMaxSize requires a positive byte cap")
	}
	return func(c *config) { c.bodyMaxSize = n }
}

// WithClock overrides the time source for tests.
//
// Panics if now is nil — RoundTrip would otherwise dereference a nil func
// on every outbound request.
func WithClock(now clock.Func) Option {
	if now == nil {
		panic("sign: WithClock requires a non-nil time source")
	}
	return func(c *config) { c.now = now }
}

// WithNonceFn overrides the nonce generator for tests.
//
// Panics if fn is nil — RoundTrip would otherwise dereference a nil func
// on every outbound request.
func WithNonceFn(fn func() string) Option {
	if fn == nil {
		panic("sign: WithNonceFn requires a non-nil nonce generator")
	}
	return func(c *config) {
		c.nonceFn = func() (string, error) {
			return fn(), nil
		}
	}
}

// Wrap returns an http.RoundTripper that adds the kit's signing
// headers to every outbound request before delegating to base.
//
// secret is the HMAC key; keyID is sent verbatim in
// X-Signature-Key-Id so the verifier can pick the right key from its
// resolver.
//
// Panics if secret is shorter than 32 bytes, keyID is empty or
// exceeds the keyID length cap, or any option is nil — all
// programmer-side wiring mistakes caught at construction.
func Wrap(base http.RoundTripper, secret []byte, keyID string, opts ...Option) http.RoundTripper {
	return wrap(base, staticKeyStore{keyID: keyID, secret: append([]byte(nil), secret...)}, opts...)
}

// WrapKeyStore returns an http.RoundTripper that resolves the current signing
// key from keys for every request. Use this with a reloading key store so new
// outbound requests move to the new key immediately while verifiers still keep
// the previous key in their resolver during the overlap window.
//
// Panics if keys is nil, its current key fails validation, or any
// option is nil. Use Wrap when a single static key is sufficient.
func WrapKeyStore(base http.RoundTripper, keys KeyStore, opts ...Option) http.RoundTripper {
	if keys == nil {
		panic("sign: KeyStore must not be nil")
	}
	return wrap(base, keys, opts...)
}

func wrap(base http.RoundTripper, keys KeyStore, opts ...Option) http.RoundTripper {
	if base == nil {
		base = transportdefaults.New(nil, 0, "httpx/sign: Wrap")
	}
	if err := validateSigningKeyStore(keys); err != nil {
		panic(err.Error())
	}
	cfg := config{
		bodyMaxSize: 10 * 1024 * 1024,
		now:         time.Now,
		nonceFn:     defaultNonce,
	}
	for _, o := range opts {
		if o == nil {
			panic("sign: Wrap option must not be nil")
		}
		o(&cfg)
	}
	return &transport{base: base, keys: keys, cfg: cfg}
}

func validateKeyID(keyID string) error {
	if keyID == "" {
		return fmt.Errorf("sign: keyID must not be empty")
	}
	if len(keyID) > keyIDMaxLen {
		return fmt.Errorf("sign: keyID exceeds maximum length")
	}
	if !httpguts.ValidHeaderFieldValue(keyID) {
		return fmt.Errorf("sign: keyID contains invalid header characters")
	}
	if strings.TrimSpace(keyID) != keyID {
		return fmt.Errorf("sign: keyID must not contain surrounding whitespace")
	}
	if strings.Contains(keyID, ",") {
		return fmt.Errorf("sign: keyID must not contain commas")
	}
	return nil
}

func validateSigningKeyStore(keys KeyStore) error {
	// Use Background here — this runs at Wrap construction time, not
	// per-request. Static stores never need a deadline, and a slow
	// remote store at construction would be a configuration bug we
	// surface at startup rather than per request.
	keyID, secret, err := keys.CurrentKeyID(context.Background())
	if err != nil {
		return fmt.Errorf("sign: KeyStore.CurrentKeyID at construction: %w", err)
	}
	return validateSigningKey(keyID, secret)
}

func validateSigningKey(keyID string, secret []byte) error {
	if len(secret) < minSecretLen {
		return errors.New("sign: secret is too short for HMAC-SHA256")
	}
	if err := validateKeyID(keyID); err != nil {
		return errors.New("sign: keyID is invalid")
	}
	return nil
}

type staticKeyStore struct {
	keyID  string
	secret []byte
}

func (s staticKeyStore) CurrentKeyID(context.Context) (string, []byte, error) {
	return s.keyID, append([]byte(nil), s.secret...), nil
}

type transport struct {
	base http.RoundTripper
	keys KeyStore
	cfg  config
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	// FR-023 [HIGH]: http.Request.Clone shares the Body field with the
	// source request, so reading clone.Body drains req.Body too.
	// Outer retry/auth middleware that re-reads the original request
	// would see an empty body. Buffer once, restore independent fresh
	// readers on BOTH req and clone, and set GetBody on the clone so
	// the standard library's redirect / 100-Continue replay path works.
	body, err := bufferBody(req, t.cfg.bodyMaxSize)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}

	clone := req.Clone(req.Context())
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	if body != nil {
		clone.Body = io.NopCloser(bytes.NewReader(body))
		clone.ContentLength = int64(len(body))
		clone.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}

	ts := strconv.FormatInt(t.cfg.now().UTC().Unix(), 10)
	nonce, err := t.cfg.nonceFn()
	if err != nil {
		return nil, err
	}
	keyID, secret, err := t.keys.CurrentKeyID(req.Context())
	if err != nil {
		return nil, fmt.Errorf("sign: resolve current key: %w", err)
	}
	if err := validateSigningKey(keyID, secret); err != nil {
		return nil, err
	}

	clone.Header.Set(signedrequest.HeaderTimestamp, ts)
	clone.Header.Set(signedrequest.HeaderNonce, nonce)
	clone.Header.Set(signedrequest.HeaderKeyID, keyID)

	sig, err := signedrequest.SignCanonical(signedrequest.SignRequest{
		Secret:          secret,
		Request:         clone,
		Timestamp:       ts,
		Nonce:           nonce,
		Body:            body,
		RequiredHeaders: t.cfg.includeHeaders,
	})
	if err != nil {
		return nil, err
	}
	clone.Header.Set(signedrequest.HeaderSignature, sig)

	return t.base.RoundTrip(clone)
}

func validateRequest(req *http.Request) error {
	if req == nil {
		return fmt.Errorf("%w: nil request", ErrInvalidRequest)
	}
	if req.URL == nil {
		return fmt.Errorf("%w: nil URL", ErrInvalidRequest)
	}
	if !httpguts.ValidHeaderFieldName(req.Method) {
		return fmt.Errorf("%w: invalid method", ErrInvalidRequest)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrInvalidRequest)
	}
	if !httpguts.ValidHostHeader(host) {
		return fmt.Errorf("%w: invalid host", ErrInvalidRequest)
	}
	if strings.ContainsAny(req.URL.RequestURI(), "\r\n") {
		return fmt.Errorf("%w: request URI contains CR/LF", ErrInvalidRequest)
	}
	return nil
}

// bufferBody drains the request body into memory (up to max bytes) and
// closes the original reader. The returned slice is suitable for both
// signing and constructing fresh independent readers for the caller's
// request and the clone the wrapper sends downstream.
//
// Returns (nil, nil) for bodyless requests (Body == nil or http.NoBody).
// Returns an error if the body exceeds max bytes — silently truncating
// would let the signed payload diverge from what the server eventually
// receives if a downstream wrapper restored more bytes from elsewhere.
func bufferBody(req *http.Request, max int64) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	buf, err := io.ReadAll(io.LimitReader(req.Body, max+1))
	closeErr := req.Body.Close()
	if err != nil {
		return nil, safeWrap("sign: read request body failed", err)
	}
	if closeErr != nil {
		return nil, safeWrap("sign: close request body failed", closeErr)
	}
	if int64(len(buf)) > max {
		return nil, errors.New("sign: body exceeds maximum size")
	}
	return buf, nil
}

// defaultNonce returns 16 random bytes base64-encoded.
func defaultNonce() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(nonceRandReader, b[:]); err != nil {
		return "", fmt.Errorf("sign: generate nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}
