// Package csrf provides session-bound CSRF token issuance and verification
// suitable for double-submit-cookie patterns.
//
// The token is HMAC-signed over (sessionID, issued-at, nonce). Verify
// checks all three:
//
//  1. HMAC integrity (constant-time).
//  2. Token age within the configured TTL.
//  3. Session binding — a token issued for session A cannot be replayed
//     against session B, even if a sibling subdomain XSS plants it via
//     Set-Cookie. This is the property the existing
//     httpx/middleware/csrf middleware lacked.
//
// The package is transport-agnostic: the [Issuer] knows nothing about
// HTTP. The httpx/middleware/csrf middleware will become a thin wrapper.
package csrf

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/clock"
	"github.com/bds421/rho-kit/core/v2/config"
)

// Token is an opaque CSRF token. The wire format is intentionally
// undocumented at the type level — callers should not parse it; they
// hand it back to [Issuer.Verify] verbatim.
type Token string

// Errors returned by [Issuer.Verify].
var (
	ErrTokenInvalid    = errors.New("csrf: invalid token")
	ErrTokenExpired    = errors.New("csrf: token expired")
	ErrSessionMismatch = errors.New("csrf: session binding mismatch")
	ErrSessionInvalid  = errors.New("csrf: session ID is invalid")
)

// DefaultTTL is the token validity window applied when [WithTTL] is not
// supplied. One hour balances usability (covers typical browsing
// sessions) against the cost of a leaked token.
const DefaultTTL = time.Hour

// MaxSessionIDLen bounds the session identifier passed to [Issuer.Issue]
// and [Issuer.Verify]. The value is large enough for opaque cookie values,
// JWT subjects, UUIDs, ULIDs, and hashed anonymous IDs while preventing a
// caller-controlled session extractor from turning every token operation
// into multi-megabyte HMAC work.
const MaxSessionIDLen = 1024

// nonceLen is the per-token random suffix length in bytes. 16 bytes of
// entropy is enough that a brute-force attacker would have to forge
// 2^64 tokens to guess one.
const nonceLen = 16

// MaxTokenLen caps the encoded length of a CSRF token Verify will
// accept. A valid token is exactly 4*ceil((8+8+16+32)/3) = 86 base64
// characters; the 256-byte cap gives 3× headroom for any future field
// addition while stopping a hostile caller from sending a multi-MB
// header that would force a costly base64 decode allocation before
// the length-mismatch check at line 199 ever runs.
const MaxTokenLen = 256

// sessionPrefixLen is the number of bytes of the session-ID hash
// embedded in the token. It is pure defense-in-depth: the HMAC already
// binds the full length-prefixed session ID (see [computeMAC]), so a
// token whose MAC verifies against a given session necessarily carries
// the matching prefix. The prefix is therefore NOT the source of the
// session-binding guarantee — it is a redundant cross-check that would
// only ever diverge from the MAC under a SHA-256/HMAC collision. Future
// readers must not treat it as load-bearing.
const sessionPrefixLen = 8

// Issuer signs and verifies CSRF tokens.
type Issuer struct {
	secrets [][]byte
	ttl     time.Duration
	now     clock.Func
}

// Option configures an [Issuer].
type Option func(*Issuer)

// WithTTL sets the validity window. Default: [DefaultTTL].
func WithTTL(d time.Duration) Option {
	return func(i *Issuer) { i.ttl = d }
}

// WithClock overrides the time source. Test-only — production callers
// should not need this. Panics on nil to fail fast at construction
// rather than dereferencing a nil func on the first Issue/Verify call.
func WithClock(now clock.Func) Option {
	if now == nil {
		panic("csrf: WithClock requires a non-nil time source")
	}
	return func(i *Issuer) { i.now = now }
}

// NewIssuer creates an Issuer with the given HMAC secret. The secret
// must be at least 32 bytes — short secrets weaken the HMAC and are
// rejected to fail loudly at startup rather than silently shipping a
// brute-forceable token format.
func NewIssuer(secret []byte, opts ...Option) (*Issuer, error) {
	return NewIssuerWithSecrets(secret, nil, opts...)
}

// NewIssuerWithSecrets creates an Issuer with one active signing secret and
// zero or more previous verification-only secrets. Issue always signs with the
// current secret; Verify accepts tokens signed by any configured secret until
// callers remove the old values after their overlap window.
func NewIssuerWithSecrets(current []byte, previous [][]byte, opts ...Option) (*Issuer, error) {
	secrets, err := cloneSecretRing(current, previous...)
	if err != nil {
		return nil, err
	}
	i := &Issuer{
		secrets: secrets,
		ttl:     DefaultTTL,
		now:     time.Now,
	}
	for _, o := range opts {
		if o == nil {
			panic("csrf: NewIssuer option must not be nil")
		}
		o(i)
	}
	if i.ttl <= 0 {
		return nil, errors.New("csrf: TTL must be positive")
	}
	return i, nil
}

// MustNewIssuer is the panic variant of [NewIssuer]. Use only at
// process startup where a missing secret is a deployment bug, not a
// runtime condition.
func MustNewIssuer(secret []byte, opts ...Option) *Issuer {
	i, err := NewIssuer(secret, opts...)
	if err != nil {
		panic("csrf: MustNewIssuer issuer configuration is invalid")
	}
	return i
}

// MustNewIssuerWithSecrets is the panic variant of [NewIssuerWithSecrets].
func MustNewIssuerWithSecrets(current []byte, previous [][]byte, opts ...Option) *Issuer {
	i, err := NewIssuerWithSecrets(current, previous, opts...)
	if err != nil {
		panic("csrf: MustNewIssuerWithSecrets issuer configuration is invalid")
	}
	return i
}

// Issue returns a fresh token bound to sessionID. The same sessionID produces
// a different token each call (the nonce ensures freshness), but every issued
// token verifies against the same sessionID.
//
// sessionID must be a non-empty, bounded, printable token. For unauthenticated
// forms, bind to a stable anonymous identifier rather than the empty string.
func (i *Issuer) Issue(sessionID string) (Token, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return "", err
	}
	now := i.now().UTC().Unix()
	if now < 0 {
		return "", fmt.Errorf("csrf: clock skew before unix epoch")
	}

	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("csrf: read nonce: %w", err)
	}

	prefix := sessionPrefix(sessionID)

	// Layout: prefix(8) || iat(8) || nonce(16) || mac(32) = 64 bytes
	body := make([]byte, 0, sessionPrefixLen+8+nonceLen)
	body = append(body, prefix...)
	body = binary.BigEndian.AppendUint64(body, uint64(now))
	body = append(body, nonce...)

	mac := computeMAC(i.secrets[0], sessionID, now, nonce)
	full := append(body, mac...)

	return Token(base64.RawURLEncoding.EncodeToString(full)), nil
}

// Verify returns nil on success, or one of [ErrTokenInvalid],
// [ErrTokenExpired], [ErrSessionMismatch], [ErrSessionInvalid].
//
// The HMAC compare is constant-time; the session-binding check is also
// constant-time over the prefix bytes. Callers should not branch on
// which specific sentinel was returned — log the error verbatim.
func (i *Issuer) Verify(t Token, sessionID string) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}
	if len(t) > MaxTokenLen {
		return ErrTokenInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(t))
	if err != nil {
		return ErrTokenInvalid
	}
	if len(raw) != sessionPrefixLen+8+nonceLen+sha256.Size {
		return ErrTokenInvalid
	}

	prefix := raw[:sessionPrefixLen]
	iat := int64(binary.BigEndian.Uint64(raw[sessionPrefixLen : sessionPrefixLen+8]))
	nonce := raw[sessionPrefixLen+8 : sessionPrefixLen+8+nonceLen]
	mac := raw[sessionPrefixLen+8+nonceLen:]

	// FR-022 [MED]: verify the HMAC FIRST, then the session prefix.
	// Pre-fix the prefix check returned ErrSessionMismatch before the
	// HMAC ran, exposing a session-prefix oracle to a caller that
	// could distinguish the two error returns or measure timing.
	// Computing the MAC unconditionally folds the session-prefix
	// signal into the HMAC's constant-time check.
	macOK := false
	for _, secret := range i.secrets {
		expectedMAC := computeMAC(secret, sessionID, iat, nonce)
		if hmac.Equal(mac, expectedMAC) {
			macOK = true
		}
	}

	expectedPrefix := sessionPrefix(sessionID)
	prefixOK := subtle.ConstantTimeCompare(prefix, expectedPrefix) == 1

	if !macOK {
		return ErrTokenInvalid
	}
	// Defense-in-depth only. The MAC above already binds the full session
	// ID, so for any token whose MAC verifies this prefix necessarily
	// matches; ErrSessionMismatch is therefore unreachable for legitimately
	// decoded tokens (it would require a SHA-256/HMAC collision). Kept as a
	// belt-and-suspenders cross-check, not as the session-binding guarantee.
	if !prefixOK {
		return ErrSessionMismatch
	}

	now := i.now().UTC().Unix()
	if now-iat > int64(i.ttl.Seconds()) {
		return ErrTokenExpired
	}
	// Reject "future" tokens beyond a small clock-skew window. Without
	// this, an attacker with a stolen secret could mint tokens valid
	// far into the future to extend the window for replay.
	if iat-now > int64(60) {
		return ErrTokenInvalid
	}

	return nil
}

func computeMAC(secret []byte, sessionID string, iat int64, nonce []byte) []byte {
	h := hmac.New(sha256.New, secret)
	// Length-prefixed sessionID so an attacker can't construct an alias
	// by splitting the field boundary differently (collisions when the
	// MAC input is just sessionID || iat || nonce).
	var lp4 [4]byte
	binary.BigEndian.PutUint32(lp4[:], uint32(len(sessionID)))
	h.Write(lp4[:])
	h.Write([]byte(sessionID))
	var lp8 [8]byte
	binary.BigEndian.PutUint64(lp8[:], uint64(iat))
	h.Write(lp8[:])
	h.Write(nonce)
	return h.Sum(nil)
}

func cloneSecretRing(current []byte, previous ...[]byte) ([][]byte, error) {
	if len(current) < 32 {
		return nil, fmt.Errorf("csrf: secret must be at least 32 bytes")
	}
	secrets := make([][]byte, 0, 1+len(previous))
	secrets = append(secrets, append([]byte(nil), current...))
	for _, secret := range previous {
		if len(secret) < 32 {
			return nil, fmt.Errorf("csrf: secret must be at least 32 bytes")
		}
		secrets = append(secrets, append([]byte(nil), secret...))
	}
	return secrets, nil
}

func sessionPrefix(sessionID string) []byte {
	h := sha256.Sum256([]byte(sessionID))
	return h[:sessionPrefixLen]
}

// ValidateSessionID validates the identifier used for session-bound token
// issuance and verification.
func ValidateSessionID(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("%w: must not be empty", ErrSessionInvalid)
	}
	if len(sessionID) > MaxSessionIDLen {
		return fmt.Errorf("%w: exceeds maximum length", ErrSessionInvalid)
	}
	if strings.TrimSpace(sessionID) != sessionID {
		return fmt.Errorf("%w: contains leading or trailing whitespace", ErrSessionInvalid)
	}
	if !utf8.ValidString(sessionID) {
		return fmt.Errorf("%w: contains invalid UTF-8", ErrSessionInvalid)
	}
	for _, r := range sessionID {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%w: contains whitespace or control characters", ErrSessionInvalid)
		}
	}
	return nil
}

// OriginAllowlist evaluates an HTTP request's Origin/Referer against a
// configured allowlist. Used as a complement to token verification —
// both signals must pass for a request to be considered safe.
type OriginAllowlist struct {
	origins map[string]struct{}
}

// NewOriginAllowlist accepts canonical scheme://host[:port] entries.
// Comparison is case-insensitive on scheme and host; ports must match
// exactly. An empty allowlist rejects everything.
func NewOriginAllowlist(origins ...string) *OriginAllowlist {
	m := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		canonical, ok := canonicalOrigin(o, false)
		if !ok {
			panic("csrf: NewOriginAllowlist invalid origin allowlist entry")
		}
		m[canonical] = struct{}{}
	}
	return &OriginAllowlist{origins: m}
}

// Allowed checks the request's Origin header (preferred) or Referer
// (fallback). Returns false if neither is present or the value isn't on
// the allowlist.
func (a *OriginAllowlist) Allowed(origin, referer string) bool {
	switch {
	case origin != "":
		return a.matches(origin)
	case referer != "":
		return a.matches(referer)
	default:
		return false
	}
}

func (a *OriginAllowlist) matches(raw string) bool {
	candidate, ok := canonicalOrigin(raw, true)
	if !ok {
		return false
	}
	_, ok = a.origins[candidate]
	return ok
}

func canonicalOrigin(raw string, allowPath bool) (string, bool) {
	if raw == "" || !utf8.ValidString(raw) {
		return "", false
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return "", false
	}
	for _, r := range raw {
		if unicode.IsSpace(r) {
			return "", false
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", false
	}
	if u.Host == "" || u.User != nil {
		return "", false
	}
	if !allowPath && (u.Path != "" || u.RawQuery != "" || u.Fragment != "") {
		return "", false
	}
	if err := config.ValidateURLHost("csrf origin", u); err != nil {
		return "", false
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), true
}
