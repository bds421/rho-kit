package reqsign

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/bds421/rho-kit/crypto/v2/signing"
	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
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

// ErrNilNonceStore is returned when verification is attempted without
// a NonceStore. This is a wiring error, not a runtime condition.
var ErrNilNonceStore = errors.New("reqsign: NonceStore must not be nil")

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
// A nil Signer is ignored and the package-level default is kept.
func WithSigner(s *signing.Signer) SignOption {
	return func(c *signConfig) {
		if s != nil {
			c.signer = s
		}
	}
}

// WithVerifySigner sets a custom signing.Signer for verification operations.
// Useful for deterministic testing with signing.WithClock.
// A nil Signer is ignored and the package-level default is kept.
func WithVerifySigner(s *signing.Signer) VerifyOption {
	return func(c *verifyConfig) {
		if s != nil {
			c.signer = s
		}
	}
}

// WithMaxAge sets the maximum allowed age for a signature.
// Values <= 0 are ignored and the default (signing.DefaultSignatureMaxAge, 5 minutes) is used.
func WithMaxAge(d time.Duration) VerifyOption {
	return func(c *verifyConfig) {
		if d > 0 {
			c.maxAge = d
		}
	}
}

// WithSignMaxBodySize sets the maximum request body size for signing.
// Values <= 0 are ignored and the default (MaxBodySize, 1 MiB) is used.
func WithSignMaxBodySize(n int64) SignOption {
	return func(c *signConfig) {
		if n > 0 {
			c.maxBodySize = n
		}
	}
}

// WithVerifyMaxBodySize sets the maximum request body size for verification.
// Values <= 0 are ignored and the default (MaxBodySize, 1 MiB) is used.
func WithVerifyMaxBodySize(n int64) VerifyOption {
	return func(c *verifyConfig) {
		if n > 0 {
			c.maxBodySize = n
		}
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
// METHOD + "\n" + REQUEST_URI + "\n" + hex(sha256(body)) + "\n" + NONCE
//
// REQUEST_URI includes the path and query string (e.g. "/api/deploy?env=prod"),
// preventing signature replay with different query parameters. The
// nonce is appended (audit FR-025) so the verifier can require it via
// the NonceStore: an attacker re-presenting the same wire bytes hits
// the store and is rejected, regardless of timestamp validity.
func canonicalBytes(method, requestURI string, body []byte, nonce string) []byte {
	h := sha256.Sum256(body)
	// Pre-allocate: method + \n + requestURI + \n + 64 hex chars + \n + nonce
	canonical := make([]byte, 0, len(method)+1+len(requestURI)+1+sha256.Size*2+1+len(nonce))
	canonical = append(canonical, method...)
	canonical = append(canonical, '\n')
	canonical = append(canonical, requestURI...)
	canonical = append(canonical, '\n')
	canonical = hex.AppendEncode(canonical, h[:])
	canonical = append(canonical, '\n')
	canonical = append(canonical, nonce...)
	return canonical
}

// generateNonce returns a 16-byte random nonce, base64-encoded. Panics
// if crypto/rand fails: a fall-through to a constant nonce would defeat
// replay protection for every request the process serves.
func generateNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("reqsign: crypto/rand failed: %v", err))
	}
	return base64.StdEncoding.EncodeToString(b[:])
}

// SignRequest signs an HTTP request using the given key store.
// It builds canonical bytes from the request method, request URI (path and
// query string), body, and a freshly-generated nonce, then delegates to
// signing.Signer.Sign for HMAC computation. The signature, timestamp,
// key ID, and nonce are set as request headers.
//
// FR-025 [HIGH]: the nonce is the wire-level token the verifier records
// in its NonceStore so a captured signed request cannot be replayed
// within its maxAge window. Each call generates a fresh nonce — do not
// retry by re-sending the same headers, sign again instead.
func SignRequest(req *http.Request, body []byte, store signing.KeyStore, opts ...SignOption) error {
	if store == nil {
		return ErrNilKeyStore
	}

	cfg := signConfig{
		signer:      defaultSigner,
		maxBodySize: MaxBodySize,
	}
	for _, o := range opts {
		o(&cfg)
	}

	if int64(len(body)) > cfg.maxBodySize {
		return fmt.Errorf("%w: %d > %d", ErrBodyTooLarge, len(body), cfg.maxBodySize)
	}

	// Use unsafe access when available to avoid allocation on the hot path.
	var keyID string
	var secret []byte
	if uks, ok := store.(signing.UnsafeKeyStore); ok {
		keyID, secret = uks.CurrentKeyUnsafe()
	} else {
		keyID, secret = store.CurrentKeyID()
	}

	nonce := generateNonce()
	canonical := canonicalBytes(req.Method, req.URL.RequestURI(), body, nonce)

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
	if int64(len(body)) > cfg.maxBodySize {
		return fmt.Errorf("%w: %d > %d", ErrBodyTooLarge, len(body), cfg.maxBodySize)
	}
	if cfg.nonceStore == nil {
		return ErrNilNonceStore
	}

	sig := req.Header.Get(HeaderSignature)
	tsStr := req.Header.Get(HeaderTimestamp)
	keyID := req.Header.Get(HeaderKeyID)
	nonce := req.Header.Get(HeaderNonce)

	if sig == "" || tsStr == "" || keyID == "" {
		return ErrMissingHeaders
	}
	if nonce == "" {
		return ErrNonceMissing
	}
	if len(nonce) > nonceMaxLen {
		return ErrNonceTooLong
	}

	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return fmt.Errorf("reqsign: invalid timestamp: %w", err)
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

	canonical := canonicalBytes(req.Method, req.URL.RequestURI(), body, nonce)

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
		o(&cfg)
	}

	return verifyRequestWithConfig(req, body, store, cfg)
}
