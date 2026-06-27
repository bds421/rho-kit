package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/clock"
)

// ErrEmptySecret is returned when an empty or too-short secret is passed to Sign or Verify.
var ErrEmptySecret = errors.New("signing: secret must be at least 32 bytes")

// minSecretLen is the minimum secret length for HMAC-SHA256 (matches hash output size).
const minSecretLen = 32

// ErrSignatureExpired is returned by Verify when the signature
// timestamp is older than maxAge — i.e. the producer's clock is behind
// the verifier's or the message has been sitting around past the
// replay window.
var ErrSignatureExpired = errors.New("signing: signature timestamp older than maxAge")

// ErrSignatureClockSkew is returned by Verify when the signature
// timestamp is too far in the future of the verifier's clock — beyond
// the allowed [WithFutureSkew] tolerance. Distinguishing this from
// [ErrSignatureExpired] lets operators alert separately on the two
// failure modes: an expired signature usually means a slow consumer
// or a replay attack; a clock-skew rejection usually means the
// producer's NTP is broken.
var ErrSignatureClockSkew = errors.New("signing: signature timestamp too far in the future")

// ErrInvalidSigner is returned when a nil or zero-value Signer is used.
var ErrInvalidSigner = errors.New("signing: invalid signer")

// ErrInvalidMaxAge is returned when Verify receives a non-positive maximum age.
var ErrInvalidMaxAge = errors.New("signing: maxAge must be positive")

// ErrInvalidContext is returned when canonical context fields cannot be safely encoded.
var ErrInvalidContext = errors.New("signing: invalid canonical context")

// ErrInvalidSignature is returned by [Signer.Verify] and [Signer.VerifyContext]
// when the supplied signature does not match the secret/body/timestamp tuple.
// The error-only return shape (replacing the prior (bool, error) signature)
// prevents callers from writing `ok, _ := Verify(...); if ok { ... }`, which
// silently treated configuration errors as "signature invalid" — a class of
// security regression.
var ErrInvalidSignature = errors.New("signing: signature does not match")

// Secret marks bytes intended for the HMAC key position. It is a distinct type
// so Sign/Verify call sites cannot accidentally swap body and secret byte
// slices without an explicit conversion.
type Secret []byte

// NewSecret returns a caller-owned copy of secret tagged for signing APIs.
func NewSecret(secret []byte) Secret {
	return append(Secret(nil), secret...)
}

// Signer holds configuration for computing and verifying HMAC-SHA256 signatures.
type Signer struct {
	clock      clock.Func
	futureSkew time.Duration
}

// SignerOption configures a Signer.
type SignerOption func(*Signer)

// WithClock sets the time source for signing. Useful for deterministic
// testing. Panics on nil to fail fast at construction rather than
// dereferencing a nil func on the first Sign/Verify call.
func WithClock(fn clock.Func) SignerOption {
	if fn == nil {
		panic("signing: WithClock requires a non-nil time source")
	}
	return func(s *Signer) { s.clock = fn }
}

// WithFutureSkew sets how far the sender's clock may be ahead of the
// verifier's without rejecting the signature. Default: 30s.
//
// Set to 0 for strict mode (any future-dated signature is rejected; useful
// when both ends share a clock, e.g. service mesh with NTP discipline).
// Use higher values for integrations against clients with poor clock
// hygiene (mobile, browser timestamps, embedded devices).
func WithFutureSkew(d time.Duration) SignerOption {
	if d < 0 {
		panic("signing: WithFutureSkew requires a non-negative duration")
	}
	return func(s *Signer) {
		s.futureSkew = d
	}
}

// NewSigner creates a Signer with the given options.
func NewSigner(opts ...SignerOption) *Signer {
	s := &Signer{
		clock:      time.Now,
		futureSkew: defaultFutureSkew,
	}
	for _, o := range opts {
		if o == nil {
			panic("signing: NewSigner option must not be nil")
		}
		o(s)
	}
	return s
}

// Sign computes an HMAC-SHA256 signature using the Signer's clock.
//
// The (secret, body) argument order matches [Signer.Verify] so paired
// call sites cannot accidentally transpose the two byte slices.
func (s *Signer) Sign(secret Secret, body []byte) (signature string, timestamp int64, err error) {
	return s.SignContext(CanonicalContext{}, secret, body)
}

// CanonicalContext binds additional out-of-band fields into the signed
// payload so a signature is only valid for the (method, path, domain)
// triple it was issued for. Use the empty value for legacy contract
// compatibility (Stripe-webhook-style signing).
//
// Recommended usage:
//
//	ctx := signing.CanonicalContext{
//	    Method: "POST",
//	    Path:   "/v1/webhooks/incoming",
//	    Domain: "myservice.webhook.v1",
//	}
//	secret := signing.NewSecret(secretBytes)
//	sig, ts, err := signer.SignContext(ctx, secret, body)
//
// Without a CanonicalContext, a signature for `<ts>.<body>` is portable
// across any endpoint that accepts the same key — a sloppy KeyResolver
// can let an attacker replay a signed POST body to a different endpoint.
type CanonicalContext struct {
	// Method is the HTTP method ("POST", "PUT", etc.). Empty omits.
	Method string
	// Path is the URL path including any query string the receiver
	// accepts as canonical. Empty omits.
	Path string
	// Domain is a free-form domain separator the producer chooses
	// (typically "<service>.<purpose>.<version>"). Empty omits.
	Domain string
}

// SignContext is Sign with an explicit canonical-context binding. See
// [CanonicalContext] for the recommended fields. Passing the zero
// CanonicalContext is identical to [Signer.Sign].
func (s *Signer) SignContext(ctx CanonicalContext, secret Secret, body []byte) (signature string, timestamp int64, err error) {
	if len(secret) < minSecretLen {
		return "", 0, ErrEmptySecret
	}
	if s == nil || s.clock == nil {
		return "", 0, ErrInvalidSigner
	}
	if err := validateCanonicalContext(ctx); err != nil {
		return "", 0, err
	}
	timestamp = s.clock().Unix()
	payload := buildSignedPayload(ctx, timestamp, body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil)), timestamp, nil
}

// buildSignedPayload assembles the bytes to MAC. The format is:
//   - With empty CanonicalContext:   "<ts>.<body>"  (legacy)
//   - With any non-empty field:      "v2.<ts>.<method>\n<path>\n<domain>\n<body>"
//
// The "v2." prefix prevents cross-version downgrade — a v2 signature can
// never be misinterpreted as a v1 (legacy) signature because the
// timestamp bytes do not collide with "v2.".
func buildSignedPayload(ctx CanonicalContext, ts int64, body []byte) []byte {
	if ctx.Method == "" && ctx.Path == "" && ctx.Domain == "" {
		out := fmt.Appendf(nil, "%d.", ts)
		return append(out, body...)
	}
	out := fmt.Appendf(nil, "v2.%d.", ts)
	out = append(out, ctx.Method...)
	out = append(out, '\n')
	out = append(out, ctx.Path...)
	out = append(out, '\n')
	out = append(out, ctx.Domain...)
	out = append(out, '\n')
	return append(out, body...)
}

// defaultSigner is the package-level Signer using time.Now as clock source.
var defaultSigner = NewSigner()

// FormatStripeStyle formats a Unix timestamp and raw HMAC digest as a
// Stripe-style header value: t=<unix>,v1=<hex>.
func FormatStripeStyle(ts int64, mac []byte) string {
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac))
}

// Sign computes an HMAC-SHA256 signature for the given body and secret,
// including a Unix timestamp in the signed payload to prevent replay attacks.
// The signed payload is: "<timestamp>.<body>".
//
// Returns [ErrEmptySecret] if secret is empty.
//
// Note: No nonce is included — within the maxAge window, a valid signature can
// be replayed. This follows the same model as Stripe webhook signatures.
// Callers requiring within-window replay prevention must maintain a
// seen-timestamp/nonce set externally.
func Sign(secret Secret, body []byte) (signature string, timestamp int64, err error) {
	return defaultSigner.Sign(secret, body)
}

// DefaultSignatureMaxAge is the default maximum age for webhook signatures.
const DefaultSignatureMaxAge = 5 * time.Minute

// defaultFutureSkew tolerates small clock differences where the sender's
// clock is slightly ahead of the receiver's. Without this, NTP jitter
// causes spurious signature verification failures. Override with
// [WithFutureSkew].
const defaultFutureSkew = 30 * time.Second

// fallbackMAC is a pre-allocated zero buffer matching SHA-256 output size.
// Used as the comparison operand when the signature has a format error (missing
// prefix, invalid hex), ensuring constant-time comparison runs regardless of
// input validity — this prevents timing side-channels that distinguish format
// errors from HMAC mismatches. It must stay all-zero: fallbackMAC[:] aliases
// this shared backing array (slicing copies only the slice header, not the
// bytes), so it is only safe because the comparison path reads it via
// hmac.Equal and never writes through the slice. Do not write through any slice
// of this array.
var fallbackMAC [sha256.Size]byte

// Verify checks an HMAC-SHA256 signature using the Signer's clock for age
// calculation (enabling deterministic testing). Uses constant-time comparison.
// The timestamp and body are combined as "<timestamp>.<body>" to match Sign.
// It rejects signatures older than maxAge to limit the replay window.
// A small future clock skew (30s) is tolerated for NTP jitter.
// Use DefaultSignatureMaxAge for a reasonable default.
//
// Returns nil on a valid signature; [ErrInvalidSignature] on a well-formed
// but mismatched signature; or one of [ErrEmptySecret], [ErrInvalidSigner],
// [ErrInvalidMaxAge], [ErrInvalidContext], [ErrSignatureClockSkew],
// [ErrSignatureExpired] when the verification could not be performed.
// Callers can distinguish "signature is bad" from "configuration is bad"
// via [errors.Is].
func (s *Signer) Verify(secret Secret, body []byte, timestamp int64, signature string, maxAge time.Duration) error {
	return s.VerifyContext(CanonicalContext{}, secret, body, timestamp, signature, maxAge)
}

// VerifyContext is Verify with an explicit [CanonicalContext]. Pass the
// same context the producer used in [Signer.SignContext] — a mismatch
// (e.g. wrong method or path) fails verification.
//
// Returns nil on success or [ErrInvalidSignature] on mismatch — see
// [Signer.Verify] for the configuration-error sentinels.
func (s *Signer) VerifyContext(ctx CanonicalContext, secret Secret, body []byte, timestamp int64, signature string, maxAge time.Duration) error {
	if len(secret) < minSecretLen {
		return ErrEmptySecret
	}
	if s == nil || s.clock == nil {
		return ErrInvalidSigner
	}
	if maxAge <= 0 {
		return ErrInvalidMaxAge
	}
	if err := validateCanonicalContext(ctx); err != nil {
		return err
	}
	age := s.clock().Sub(time.Unix(timestamp, 0))
	if age < -s.futureSkew {
		return ErrSignatureClockSkew
	}
	if age > maxAge {
		return ErrSignatureExpired
	}

	payload := buildSignedPayload(ctx, timestamp, body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expectedRaw := mac.Sum(nil)

	// Always decode and compare in a single code path regardless of format
	// errors. This eliminates timing differences between format errors and
	// HMAC mismatches.
	//
	// FR-047 [LOW]: hmac.Equal returns immediately on length mismatch,
	// so a "valid hex of the wrong length" signature would short-circuit
	// the comparison. Force the comparison input to exactly sha256.Size
	// bytes — using fallbackMAC for any decoded value of a different
	// length — so every input takes the same code path.
	const prefix = "sha256="
	gotRaw := fallbackMAC[:]
	if len(signature) >= len(prefix) && signature[:len(prefix)] == prefix {
		if decoded, err := hex.DecodeString(signature[len(prefix):]); err == nil && len(decoded) == sha256.Size {
			gotRaw = decoded
		}
	}
	if !hmac.Equal(expectedRaw, gotRaw) {
		return ErrInvalidSignature
	}
	return nil
}

func validateCanonicalContext(ctx CanonicalContext) error {
	if strings.ContainsAny(ctx.Method, "\r\n") {
		return fmt.Errorf("%w: Method must not contain CR/LF", ErrInvalidContext)
	}
	if strings.ContainsAny(ctx.Path, "\r\n") {
		return fmt.Errorf("%w: Path must not contain CR/LF", ErrInvalidContext)
	}
	if strings.ContainsAny(ctx.Domain, "\r\n") {
		return fmt.Errorf("%w: Domain must not contain CR/LF", ErrInvalidContext)
	}
	return nil
}

// Verify checks an HMAC-SHA256 signature using constant-time comparison.
// See [Signer.Verify] for the full contract — package-level convenience
// using the default signer's time.Now clock.
func Verify(secret Secret, body []byte, timestamp int64, signature string, maxAge time.Duration) error {
	return defaultSigner.Verify(secret, body, timestamp, signature, maxAge)
}

// SignContext is the package-level [Signer.SignContext], using the
// default signer's time.Now clock.
func SignContext(ctx CanonicalContext, secret Secret, body []byte) (signature string, timestamp int64, err error) {
	return defaultSigner.SignContext(ctx, secret, body)
}

// VerifyContext is the package-level [Signer.VerifyContext], using the
// default signer's time.Now clock.
func VerifyContext(ctx CanonicalContext, secret Secret, body []byte, timestamp int64, signature string, maxAge time.Duration) error {
	return defaultSigner.VerifyContext(ctx, secret, body, timestamp, signature, maxAge)
}
