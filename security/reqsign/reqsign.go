package reqsign

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/bds421/rho-kit/crypto/signing"
)

const (
	// HeaderSignature is the HTTP header containing the HMAC-SHA256 signature.
	HeaderSignature = "X-Signature"
	// HeaderTimestamp is the HTTP header containing the Unix timestamp.
	HeaderTimestamp = "X-Signature-Timestamp"
	// HeaderKeyID is the HTTP header identifying which key was used.
	HeaderKeyID = "X-Signature-KeyID"

	// MaxBodySize is the default maximum request body size (1 MiB) that the
	// package will buffer for signing or verification.
	MaxBodySize = 1 << 20

	// nilKeyStoreMsg is the panic message used when a nil KeyStore is passed.
	nilKeyStoreMsg = "reqsign: KeyStore must not be nil"
)

// ErrMissingHeaders is returned when required signature headers are absent.
var ErrMissingHeaders = errors.New("reqsign: missing signature headers")

// ErrKeyNotFound is returned when the key ID from the request is not in the store.
var ErrKeyNotFound = errors.New("reqsign: key ID not found")

// ErrSignatureMismatch is returned when the computed HMAC does not match the
// signature provided in the request.
var ErrSignatureMismatch = errors.New("reqsign: signature mismatch")

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

// canonicalBytes builds the canonical representation of an HTTP request:
// METHOD + "\n" + REQUEST_URI + "\n" + hex(sha256(body))
//
// REQUEST_URI includes the path and query string (e.g. "/api/deploy?env=prod"),
// preventing signature replay with different query parameters.
func canonicalBytes(method, requestURI string, body []byte) []byte {
	h := sha256.Sum256(body)
	// Pre-allocate: method + \n + requestURI + \n + 64 hex chars
	canonical := make([]byte, 0, len(method)+1+len(requestURI)+1+sha256.Size*2)
	canonical = append(canonical, method...)
	canonical = append(canonical, '\n')
	canonical = append(canonical, requestURI...)
	canonical = append(canonical, '\n')
	canonical = hex.AppendEncode(canonical, h[:])
	return canonical
}

// SignRequest signs an HTTP request using the given key store.
// It builds canonical bytes from the request method, request URI (path and
// query string), and body, then delegates to signing.Signer.Sign for HMAC
// computation. The signature, timestamp, and key ID are set as request headers.
func SignRequest(req *http.Request, body []byte, store KeyStore, opts ...SignOption) error {
	if store == nil {
		panic(nilKeyStoreMsg)
	}

	cfg := signConfig{
		signer:      defaultSigner,
		maxBodySize: MaxBodySize,
	}
	for _, o := range opts {
		o(&cfg)
	}

	// Use unsafe access for StaticKeyStore to avoid allocation.
	var keyID string
	var secret []byte
	if sks, ok := store.(*StaticKeyStore); ok {
		keyID, secret = sks.currentKeyUnsafe()
	} else {
		keyID, secret = store.CurrentKeyID()
	}

	canonical := canonicalBytes(req.Method, req.URL.RequestURI(), body)

	sig, ts, err := cfg.signer.Sign(canonical, secret)
	if err != nil {
		return fmt.Errorf("reqsign: sign failed: %w", err)
	}

	req.Header.Set(HeaderSignature, sig)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderKeyID, keyID)
	return nil
}

// VerifyRequest verifies the signature on an incoming HTTP request.
// It reads the signature headers, looks up the key by ID from the store,
// builds canonical bytes, and delegates to signing.Signer.Verify.
func VerifyRequest(req *http.Request, body []byte, store KeyStore, opts ...VerifyOption) error {
	if store == nil {
		panic(nilKeyStoreMsg)
	}

	cfg := verifyConfig{
		signer:      defaultSigner,
		maxAge:      signing.DefaultSignatureMaxAge,
		maxBodySize: MaxBodySize,
	}
	for _, o := range opts {
		o(&cfg)
	}

	sig := req.Header.Get(HeaderSignature)
	tsStr := req.Header.Get(HeaderTimestamp)
	keyID := req.Header.Get(HeaderKeyID)

	if sig == "" || tsStr == "" || keyID == "" {
		return ErrMissingHeaders
	}

	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return fmt.Errorf("reqsign: invalid timestamp: %w", err)
	}

	// Use unsafe access for StaticKeyStore to avoid allocation.
	var secret []byte
	var ok bool
	if sks, castOK := store.(*StaticKeyStore); castOK {
		secret, ok = sks.keyUnsafe(keyID)
	} else {
		secret, ok = store.Key(keyID)
	}
	if !ok {
		return ErrKeyNotFound
	}

	canonical := canonicalBytes(req.Method, req.URL.RequestURI(), body)

	valid, err := cfg.signer.Verify(secret, canonical, ts, sig, cfg.maxAge)
	if err != nil {
		return fmt.Errorf("reqsign: verify failed: %w", err)
	}
	if !valid {
		return ErrSignatureMismatch
	}
	return nil
}
