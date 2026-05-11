package reqsign

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bds421/rho-kit/crypto/v2/signing"
	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
	"golang.org/x/net/http/httpguts"
)

const (
	// HeaderSignature is the HTTP header containing the HMAC-SHA256 signature.
	HeaderSignature = "X-Signature"
	// HeaderTimestamp is the HTTP header containing the Unix timestamp.
	HeaderTimestamp = "X-Signature-Timestamp"
	// HeaderKeyID is the HTTP header identifying which key was used.
	HeaderKeyID = "X-Signature-KeyID"
	// HeaderNonce is the HTTP header containing a per-request random
	// nonce used for replay protection. Verifiers maintain a NonceStore
	// keyed on this value with a TTL ≥ MaxAge so an intercepted signed
	// request cannot be replayed before its timestamp expires
	// (audit FR-025).
	HeaderNonce = "X-Signature-Nonce"

	// MaxBodySize is the default maximum request body size (1 MiB) that the
	// package will buffer for signing or verification.
	MaxBodySize = 1 << 20

	// signatureMaxLen is the exact wire length of
	// "sha256="+hex(HMAC-SHA256). Rejecting longer values avoids feeding
	// unbounded attacker input into signature verification.
	signatureMaxLen = len("sha256=") + sha256.Size*2

	// timestampMaxLen is large enough for any signed int64 Unix timestamp
	// rendered in base 10, including a leading minus sign for invalid
	// client inputs that ParseInt will reject or expire.
	timestampMaxLen = 20

	// keyIDMaxLen bounds the key ID before it reaches caller-provided
	// KeyStore implementations.
	keyIDMaxLen = 256

	// nonceMaxLen caps the accepted nonce header length. Real nonces
	// are 16 random bytes base64-encoded (24 chars including padding);
	// 64 leaves room for adapters that pick a different encoding while
	// still preventing pathological nonce-store key sizes.
	nonceMaxLen = 64

	// nilKeyStoreMsg is the panic message used when a nil KeyStore is passed.
	nilKeyStoreMsg = "reqsign: KeyStore must not be nil"
	// nilNonceStoreMsg is the panic message used when a nil NonceStore
	// is passed. Replay protection must be wired or every request is
	// vulnerable to replay within the maxAge window.
	nilNonceStoreMsg = "reqsign: NonceStore must not be nil — replay-vulnerable signing is worse than no signing"
)

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

// NonceStore is the abstraction over the replay-protection cache used
// by [RequireSignedRequest]. It is the same contract as
// [signedrequest.NonceStore] — reqsign re-exports it as a type alias so
// the helper [signedrequest.NewMemoryNonceStore] is usable directly.
//
// Implementations must:
//   - Store the nonce with a TTL ≥ MaxAge.
//   - Return (true, nil) on first observation; (false, nil) on replay.
//   - Be safe for concurrent use.
type NonceStore = signedrequest.NonceStore

// ErrNilKeyStore is returned when a nil KeyStore is passed to SignRequest or VerifyRequest.
var ErrNilKeyStore = errors.New("reqsign: KeyStore must not be nil")

// ErrMissingHeaders is returned when required signature headers are absent.
var ErrMissingHeaders = errors.New("reqsign: missing signature headers")

// ErrInvalidHeaders is returned when required signature headers are present
// in an ambiguous form, such as duplicate header instances.
var ErrInvalidHeaders = errors.New("reqsign: invalid signature headers")

// ErrKeyNotFound is returned when the key ID from the request is not in the store.
var ErrKeyNotFound = errors.New("reqsign: key ID not found")

// ErrSignatureMismatch is returned when the computed HMAC does not match the
// signature provided in the request.
var ErrSignatureMismatch = errors.New("reqsign: signature mismatch")

// ErrBodyTooLarge is returned when a body exceeds the configured maximum size
// for signing or verification.
var ErrBodyTooLarge = errors.New("reqsign: body exceeds configured maximum size")

// ErrReplay is returned when a previously-seen nonce is observed by the
// verifier (audit FR-025). Replays inside the maxAge window were
// previously accepted; the nonce store closes that gap.
var ErrReplay = errors.New("reqsign: nonce already used (replay)")

// ErrNonceMissing is returned when the X-Signature-Nonce header is
// absent on an inbound request.
var ErrNonceMissing = errors.New("reqsign: missing nonce header")

// ErrNonceTooLong is returned when X-Signature-Nonce exceeds nonceMaxLen
// bytes. Capping the size prevents adversaries from inflating
// nonce-store keys to pathological lengths.
var ErrNonceTooLong = errors.New("reqsign: nonce header exceeds maximum length")

// ErrNonceInvalid is returned when X-Signature-Nonce is not the
// expected wire format: 16 random bytes encoded with a standard base64
// alphabet. Verification rejects malformed nonces before they reach a
// caller-provided NonceStore so stores are not responsible for enforcing
// the protocol boundary.
var ErrNonceInvalid = errors.New("reqsign: nonce header is malformed")

// ErrNilNonceStore is returned when verification is attempted without
// a NonceStore. This is a wiring error, not a runtime condition.
var ErrNilNonceStore = errors.New("reqsign: NonceStore must not be nil")

// ErrInvalidRequest is returned when signing or verification is asked
// to process a structurally invalid HTTP request.
var ErrInvalidRequest = errors.New("reqsign: invalid request")

// ErrTimestampInvalid is returned when the signature timestamp header is not
// a valid Unix timestamp. The raw header value is intentionally not reflected
// in the error because verification errors are commonly logged at the
// middleware boundary.
var ErrTimestampInvalid = errors.New("reqsign: invalid timestamp")

// defaultSigner is a package-level Signer reused across calls.
// signing.Signer is safe for concurrent use (it only carries a clock function).
var defaultSigner = signing.NewSigner()

// signConfig holds options for signing.
type signConfig struct {
	signer      *signing.Signer
	maxBodySize int64
}

// verifyConfig holds options for verification.
type verifyConfig struct {
	signer      *signing.Signer
	maxAge      time.Duration
	maxBodySize int64
	nonceStore  NonceStore
}

// SignOption configures request signing behavior.
type SignOption func(*signConfig)

// VerifyOption configures request verification behavior.
type VerifyOption func(*verifyConfig)

// WithSigner sets a custom signing.Signer for signing operations.
// Useful for deterministic testing with signing.WithClock.
// Panics on nil so a test-only wiring mistake cannot silently fall back to
// wall-clock signing.
func WithSigner(s *signing.Signer) SignOption {
	if s == nil {
		panic("reqsign: WithSigner requires a non-nil Signer")
	}
	return func(c *signConfig) {
		c.signer = s
	}
}

// WithVerifySigner sets a custom signing.Signer for verification operations.
// Useful for deterministic testing with signing.WithClock.
// Panics on nil so verification does not silently switch clocks.
func WithVerifySigner(s *signing.Signer) VerifyOption {
	if s == nil {
		panic("reqsign: WithVerifySigner requires a non-nil Signer")
	}
	return func(c *verifyConfig) {
		c.signer = s
	}
}

// WithMaxAge sets the maximum allowed age for a signature.
// Panics on non-positive values; omit the option to use the default
// (signing.DefaultSignatureMaxAge, 5 minutes).
func WithMaxAge(d time.Duration) VerifyOption {
	if d <= 0 {
		panic("reqsign: WithMaxAge requires a positive duration")
	}
	return func(c *verifyConfig) {
		c.maxAge = d
	}
}

// WithSignMaxBodySize sets the maximum request body size for signing.
// Panics on non-positive values; omit the option to use the default
// (MaxBodySize, 1 MiB).
func WithSignMaxBodySize(n int64) SignOption {
	if n <= 0 {
		panic("reqsign: WithSignMaxBodySize requires a positive byte cap")
	}
	return func(c *signConfig) {
		c.maxBodySize = n
	}
}

// WithVerifyMaxBodySize sets the maximum request body size for verification.
// Panics on non-positive values; omit the option to use the default
// (MaxBodySize, 1 MiB).
func WithVerifyMaxBodySize(n int64) VerifyOption {
	if n <= 0 {
		panic("reqsign: WithVerifyMaxBodySize requires a positive byte cap")
	}
	return func(c *verifyConfig) {
		c.maxBodySize = n
	}
}

// WithNonceStore wires the replay-protection store into verification.
// Audit FR-025: without a nonce store, a captured signed request can
// be replayed any number of times until its timestamp expires. The
// store retains nonces seen within the maxAge window so duplicates
// are rejected.
//
// Use [signedrequest.NewMemoryNonceStore] for single-instance
// deployments, or wire a Redis-backed implementation for multi-replica
// services. Pass nil only in tests where replay is irrelevant — the
// production wiring in [RequireSignedRequest] panics on nil.
func WithNonceStore(s NonceStore) VerifyOption {
	return func(c *verifyConfig) { c.nonceStore = s }
}

// canonicalBytes builds the canonical representation of an HTTP request:
// METHOD + "\n" + REQUEST_URI + "\n" + HOST + "\n" + CONTENT_TYPE + "\n" + hex(sha256(body)) + "\n" + NONCE
//
// REQUEST_URI includes the path and query string (e.g. "/api/deploy?env=prod"),
// preventing signature replay with different query parameters. The
// host binds signatures to the receiving authority so a valid request for
// service A cannot be replayed against service B at the same path when a key
// is accidentally reused. Content-Type is included so a signed JSON request
// cannot be re-labeled as a different media type. The nonce is appended
// (audit FR-025) so the verifier can require it via the NonceStore: an
// attacker re-presenting the same wire bytes hits the store and is rejected,
// regardless of timestamp validity.
func canonicalBytes(method, requestURI, host, contentType string, body []byte, nonce string) []byte {
	h := sha256.Sum256(body)
	// Pre-allocate: method + \n + requestURI + \n + host + \n + contentType + \n + 64 hex chars + \n + nonce
	canonical := make([]byte, 0, len(method)+1+len(requestURI)+1+len(host)+1+len(contentType)+1+sha256.Size*2+1+len(nonce))
	canonical = append(canonical, method...)
	canonical = append(canonical, '\n')
	canonical = append(canonical, requestURI...)
	canonical = append(canonical, '\n')
	canonical = append(canonical, strings.ToLower(host)...)
	canonical = append(canonical, '\n')
	canonical = append(canonical, contentType...)
	canonical = append(canonical, '\n')
	canonical = hex.AppendEncode(canonical, h[:])
	canonical = append(canonical, '\n')
	canonical = append(canonical, nonce...)
	return canonical
}

// generateNonce returns a 16-byte random nonce, base64-encoded.
func generateNonce() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(nonceRandReader, b[:]); err != nil {
		return "", fmt.Errorf("reqsign: generate nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

func validWireNonce(nonce string) bool {
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
	if strings.ContainsAny(req.URL.RequestURI(), "\r\n") {
		return fmt.Errorf("%w: request URI contains CR/LF", ErrInvalidRequest)
	}
	host := requestHost(req)
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrInvalidRequest)
	}
	if !httpguts.ValidHostHeader(host) {
		return fmt.Errorf("%w: invalid host", ErrInvalidRequest)
	}
	return nil
}

func requestHost(req *http.Request) string {
	if req.Host != "" {
		return req.Host
	}
	if req.URL != nil {
		return req.URL.Host
	}
	return ""
}

func canonicalHost(req *http.Request) string {
	return strings.ToLower(requestHost(req))
}

func canonicalContentType(req *http.Request) (string, error) {
	values := req.Header.Values("Content-Type")
	switch len(values) {
	case 0:
		return "", nil
	case 1:
		if !httpguts.ValidHeaderFieldValue(values[0]) {
			return "", ErrInvalidHeaders
		}
		return values[0], nil
	default:
		return "", ErrInvalidHeaders
	}
}

// SignRequest signs an HTTP request using the given key store.
// It builds canonical bytes from the request method, request URI (path and
// query string), host, Content-Type, body, and a freshly-generated nonce,
// then delegates to signing.Signer.Sign for HMAC computation. The signature,
// timestamp, key ID, and nonce are set as request headers.
//
// FR-025 [HIGH]: the nonce is the wire-level token the verifier records
// in its NonceStore so a captured signed request cannot be replayed
// within its maxAge window. Each call generates a fresh nonce — do not
// retry by re-sending the same headers, sign again instead.
func SignRequest(req *http.Request, body []byte, store signing.KeyStore, opts ...SignOption) error {
	if store == nil {
		return ErrNilKeyStore
	}
	if err := validateRequest(req); err != nil {
		return err
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}

	cfg := signConfig{
		signer:      defaultSigner,
		maxBodySize: MaxBodySize,
	}
	for _, o := range opts {
		if o == nil {
			panic("reqsign: sign option must not be nil")
		}
		o(&cfg)
	}

	if int64(len(body)) > cfg.maxBodySize {
		return fmt.Errorf("%w: request body exceeds maximum size", ErrBodyTooLarge)
	}
	contentType, err := canonicalContentType(req)
	if err != nil {
		return err
	}

	// Use unsafe access when available to avoid allocation on the hot path.
	var keyID string
	var secret []byte
	if uks, ok := store.(signing.UnsafeKeyStore); ok {
		keyID, secret = uks.CurrentKeyUnsafe()
	} else {
		keyID, secret = store.CurrentKeyID()
	}
	if err := validateSingletonHeaderValue(keyID, keyIDMaxLen); err != nil {
		return err
	}

	nonce, err := generateNonce()
	if err != nil {
		return err
	}
	canonical := canonicalBytes(req.Method, req.URL.RequestURI(), canonicalHost(req), contentType, body, nonce)

	sig, ts, err := cfg.signer.Sign(canonical, secret)
	if err != nil {
		return fmt.Errorf("reqsign: sign failed: %w", err)
	}

	req.Header.Set(HeaderSignature, sig)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderKeyID, keyID)
	req.Header.Set(HeaderNonce, nonce)
	return nil
}

// verifyRequestWithConfig verifies a request using a pre-built verifyConfig.
// This avoids re-applying options on every request in the middleware hot path.
//
// Verification order:
//  1. Body cap.
//  2. Header presence (signature, timestamp, key id, nonce).
//  3. Nonce length cap.
//  4. Timestamp parse + key resolve.
//  5. Signature verification (constant-time, includes nonce in canonical).
//  6. Nonce store check — only after signature passes, so an attacker
//     cannot pollute the store with arbitrary nonces.
//
// FR-025 [HIGH]: step 6 is the replay defence. Storing the nonce
// before the signature check would let an unauthenticated client fill
// the store with garbage. Storing after means only attackers with a
// valid signature (and thus the secret) can ever insert.
func verifyRequestWithConfig(req *http.Request, body []byte, store signing.KeyStore, cfg verifyConfig) error {
	if err := validateRequest(req); err != nil {
		return err
	}
	if int64(len(body)) > cfg.maxBodySize {
		return fmt.Errorf("%w: request body exceeds maximum size", ErrBodyTooLarge)
	}
	if cfg.nonceStore == nil {
		return ErrNilNonceStore
	}

	sig, err := requiredSingletonHeader(req.Header, HeaderSignature, ErrMissingHeaders, signatureMaxLen)
	if err != nil {
		return err
	}
	tsStr, err := requiredSingletonHeader(req.Header, HeaderTimestamp, ErrMissingHeaders, timestampMaxLen)
	if err != nil {
		return err
	}
	keyID, err := requiredSingletonHeader(req.Header, HeaderKeyID, ErrMissingHeaders, keyIDMaxLen)
	if err != nil {
		return err
	}
	nonce, err := requiredSingletonHeader(req.Header, HeaderNonce, ErrNonceMissing, nonceMaxLen)
	if err != nil {
		return err
	}
	if !validWireNonce(nonce) {
		return ErrNonceInvalid
	}
	contentType, err := canonicalContentType(req)
	if err != nil {
		return err
	}

	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return ErrTimestampInvalid
	}

	// Use unsafe access when available to avoid allocation on the hot path.
	var secret []byte
	var ok bool
	if uks, castOK := store.(signing.UnsafeKeyStore); castOK {
		secret, ok = uks.KeyUnsafe(keyID)
	} else {
		secret, ok = store.Key(keyID)
	}
	if !ok {
		return ErrKeyNotFound
	}

	canonical := canonicalBytes(req.Method, req.URL.RequestURI(), canonicalHost(req), contentType, body, nonce)

	valid, err := cfg.signer.Verify(secret, canonical, ts, sig, cfg.maxAge)
	if err != nil {
		return fmt.Errorf("reqsign: verify failed: %w", err)
	}
	if !valid {
		return ErrSignatureMismatch
	}

	first, err := cfg.nonceStore.SeenOrStore(nonce)
	if err != nil {
		return fmt.Errorf("reqsign: nonce store: %w", err)
	}
	if !first {
		return ErrReplay
	}
	return nil
}

func requiredSingletonHeader(h http.Header, name string, missingErr error, maxLen int) (string, error) {
	values := h.Values(name)
	switch len(values) {
	case 0:
		return "", missingErr
	case 1:
	default:
		return "", ErrInvalidHeaders
	}
	value := values[0]
	if value == "" {
		return "", missingErr
	}
	if err := validateSingletonHeaderValue(value, maxLen); err != nil {
		return "", err
	}
	return value, nil
}

func validateSingletonHeaderValue(value string, maxLen int) error {
	if value == "" {
		return ErrInvalidHeaders
	}
	if maxLen > 0 && len(value) > maxLen {
		if maxLen == nonceMaxLen {
			return ErrNonceTooLong
		}
		return ErrInvalidHeaders
	}
	if strings.TrimSpace(value) != value ||
		strings.Contains(value, ",") ||
		!httpguts.ValidHeaderFieldValue(value) {
		return ErrInvalidHeaders
	}
	return nil
}

// VerifyRequest verifies the signature on an incoming HTTP request.
// It reads the signature headers, looks up the key by ID from the store,
// builds canonical bytes, and delegates to signing.Signer.Verify.
//
// Replay protection requires a non-nil NonceStore wired via
// [WithNonceStore]; otherwise verification returns [ErrNilNonceStore]
// (audit FR-025).
func VerifyRequest(req *http.Request, body []byte, store signing.KeyStore, opts ...VerifyOption) error {
	if store == nil {
		return ErrNilKeyStore
	}

	cfg := verifyConfig{
		signer:      defaultSigner,
		maxAge:      signing.DefaultSignatureMaxAge,
		maxBodySize: MaxBodySize,
	}
	for _, o := range opts {
		if o == nil {
			panic("reqsign: verify option must not be nil")
		}
		o(&cfg)
	}

	return verifyRequestWithConfig(req, body, store, cfg)
}
