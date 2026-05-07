// Package sign provides an http.RoundTripper that signs every outbound
// request using the wire format expected by
// httpx/middleware/signedrequest.
//
//	client := &http.Client{Transport: sign.Wrap(http.DefaultTransport, secret, "prod-2026")}
//	resp, err := client.Post(url, "application/json", body)
//
// The wrapper reads the request body, computes a SHA-256 hash, signs
// the canonical string, and rewinds the body before dispatching. Body
// reading is bounded by [WithBodyMaxSize] (default 10 MiB).
package sign

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bds421/rho-kit/httpx/middleware/signedrequest"
)

// minSecretLen matches HMAC-SHA256 output size and the floor enforced by
// crypto/signing. Sub-32-byte secrets do not provide the security HMAC-SHA256
// is designed for and are rejected at construction.
const minSecretLen = 32

// Option configures the [Wrap] RoundTripper.
type Option func(*config)

type config struct {
	keyID          string
	includeHeaders []string
	bodyMaxSize    int64
	now            func() time.Time
	nonceFn        func() string
}

// WithIncludeHeaders pins additional headers into the canonical
// signing string. The names are also passed to the verifier via the
// matching WithRequiredHeaders option — the two MUST agree, otherwise
// every request fails verification.
func WithIncludeHeaders(names ...string) Option {
	return func(c *config) {
		for _, n := range names {
			c.includeHeaders = append(c.includeHeaders, strings.ToLower(n))
		}
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
func WithClock(now func() time.Time) Option {
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
	return func(c *config) { c.nonceFn = fn }
}

// Wrap returns an http.RoundTripper that adds the kit's signing
// headers to every outbound request before delegating to base.
//
// secret is the HMAC key; keyID is sent verbatim in
// X-Signature-Key-Id so the verifier can pick the right key from its
// resolver.
func Wrap(base http.RoundTripper, secret []byte, keyID string, opts ...Option) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if len(secret) < minSecretLen {
		panic(fmt.Sprintf("sign: secret must be at least %d bytes for HMAC-SHA256", minSecretLen))
	}
	if keyID == "" {
		panic("sign: keyID must not be empty")
	}
	cfg := config{
		keyID:       keyID,
		bodyMaxSize: 10 * 1024 * 1024,
		now:         time.Now,
		nonceFn:     defaultNonce,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return &transport{base: base, secret: append([]byte(nil), secret...), cfg: cfg}
}

type transport struct {
	base   http.RoundTripper
	secret []byte
	cfg    config
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readBody(req, t.cfg.bodyMaxSize)
	if err != nil {
		return nil, err
	}

	ts := strconv.FormatInt(t.cfg.now().UTC().Unix(), 10)
	nonce := t.cfg.nonceFn()

	req.Header.Set(signedrequest.HeaderTimestamp, ts)
	req.Header.Set(signedrequest.HeaderNonce, nonce)
	req.Header.Set(signedrequest.HeaderKeyID, t.cfg.keyID)

	sig := signedrequest.SignCanonical(t.secret, req, ts, nonce, body, t.cfg.includeHeaders)
	req.Header.Set(signedrequest.HeaderSignature, sig)

	return t.base.RoundTrip(req)
}

func readBody(req *http.Request, max int64) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	buf, err := io.ReadAll(io.LimitReader(req.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("sign: read request body: %w", err)
	}
	if int64(len(buf)) > max {
		return nil, fmt.Errorf("sign: body exceeds maximum %d bytes", max)
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(buf))
	req.ContentLength = int64(len(buf))
	return buf, nil
}

// defaultNonce returns 16 random bytes base64-encoded. Panics if
// crypto/rand fails — on a healthy Linux system this never happens, but
// silently falling back to all-zero bytes would defeat replay
// protection (every request would carry the same nonce). Better to
// crash than to ship a forgeable signature.
func defaultNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("sign: crypto/rand failed: %v", err))
	}
	return base64.StdEncoding.EncodeToString(b[:])
}
