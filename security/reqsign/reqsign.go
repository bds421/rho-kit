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
)

// ErrMissingHeaders is returned when required signature headers are absent.
var ErrMissingHeaders = errors.New("reqsign: missing signature headers")

// ErrKeyNotFound is returned when the key ID from the request is not in the store.
var ErrKeyNotFound = errors.New("reqsign: key ID not found")

// signConfig holds options for signing.
type signConfig struct {
	signer *signing.Signer
}

// verifyConfig holds options for verification.
type verifyConfig struct {
	signer *signing.Signer
	maxAge time.Duration
}

// SignOption configures request signing behavior.
type SignOption func(*signConfig)

// VerifyOption configures request verification behavior.
type VerifyOption func(*verifyConfig)

// WithSigner sets a custom signing.Signer for signing operations.
// Useful for deterministic testing with signing.WithClock.
func WithSigner(s *signing.Signer) SignOption {
	return func(c *signConfig) { c.signer = s }
}

// WithVerifySigner sets a custom signing.Signer for verification operations.
// Useful for deterministic testing with signing.WithClock.
func WithVerifySigner(s *signing.Signer) VerifyOption {
	return func(c *verifyConfig) { c.signer = s }
}

// WithMaxAge sets the maximum allowed age for a signature.
// Default: signing.DefaultSignatureMaxAge (5 minutes).
func WithMaxAge(d time.Duration) VerifyOption {
	return func(c *verifyConfig) { c.maxAge = d }
}

// canonicalBytes builds the canonical representation of an HTTP request:
// METHOD + "\n" + PATH + "\n" + hex(sha256(body))
func canonicalBytes(method, path string, body []byte) []byte {
	h := sha256.Sum256(body)
	// Pre-allocate: method + \n + path + \n + 64 hex chars
	canonical := make([]byte, 0, len(method)+1+len(path)+1+sha256.Size*2)
	canonical = append(canonical, method...)
	canonical = append(canonical, '\n')
	canonical = append(canonical, path...)
	canonical = append(canonical, '\n')
	canonical = hex.AppendEncode(canonical, h[:])
	return canonical
}

// SignRequest signs an HTTP request using the given key store.
// It builds canonical bytes from the request method, path, and body,
// then delegates to signing.Signer.Sign for HMAC computation.
// The signature, timestamp, and key ID are set as request headers.
func SignRequest(req *http.Request, body []byte, store KeyStore, opts ...SignOption) error {
	cfg := signConfig{signer: signing.NewSigner()}
	for _, o := range opts {
		o(&cfg)
	}

	keyID, secret := store.CurrentKeyID()
	canonical := canonicalBytes(req.Method, req.URL.Path, body)

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
	cfg := verifyConfig{
		signer: signing.NewSigner(),
		maxAge: signing.DefaultSignatureMaxAge,
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

	secret, ok := store.Key(keyID)
	if !ok {
		return ErrKeyNotFound
	}

	canonical := canonicalBytes(req.Method, req.URL.Path, body)

	valid, err := cfg.signer.Verify(secret, canonical, ts, sig, cfg.maxAge)
	if err != nil {
		return fmt.Errorf("reqsign: verify failed: %w", err)
	}
	if !valid {
		return errors.New("reqsign: signature mismatch")
	}
	return nil
}
