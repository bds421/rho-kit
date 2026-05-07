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
	"strings"
	"time"
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
)

// DefaultTTL is the token validity window applied when [WithTTL] is not
// supplied. One hour balances usability (covers typical browsing
// sessions) against the cost of a leaked token.
const DefaultTTL = time.Hour

// nonceLen is the per-token random suffix length in bytes. 16 bytes of
// entropy is enough that a brute-force attacker would have to forge
// 2^64 tokens to guess one.
const nonceLen = 16

// sessionPrefixLen is the number of bytes of the session-ID hash
// embedded in the token so [Issuer.Verify] can short-circuit on a
// mismatched session before doing the HMAC compare.
const sessionPrefixLen = 8

// Issuer signs and verifies CSRF tokens.
type Issuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// Option configures an [Issuer].
type Option func(*Issuer)

// WithTTL sets the validity window. Default: [DefaultTTL].
func WithTTL(d time.Duration) Option {
	return func(i *Issuer) { i.ttl = d }
}

// WithClock overrides the time source. Test-only — production callers
// should not need this.
func WithClock(now func() time.Time) Option {
	return func(i *Issuer) { i.now = now }
}

// NewIssuer creates an Issuer with the given HMAC secret. The secret
// must be at least 32 bytes — short secrets weaken the HMAC and are
// rejected to fail loudly at startup rather than silently shipping a
// brute-forceable token format.
func NewIssuer(secret []byte, opts ...Option) (*Issuer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("csrf: secret must be at least 32 bytes, got %d", len(secret))
	}
	i := &Issuer{
		secret: append([]byte(nil), secret...),
		ttl:    DefaultTTL,
		now:    time.Now,
	}
	for _, o := range opts {
		o(i)
	}
	if i.ttl <= 0 {
		return nil, fmt.Errorf("csrf: TTL must be positive, got %s", i.ttl)
	}
	return i, nil
}

// MustNewIssuer is the panic variant of [NewIssuer]. Use only at
// process startup where a missing secret is a deployment bug, not a
// runtime condition.
func MustNewIssuer(secret []byte, opts ...Option) *Issuer {
	i, err := NewIssuer(secret, opts...)
	if err != nil {
		panic(err)
	}
	return i
}

// Issue returns a fresh token bound to sessionID. The same sessionID
// produces a different token each call (the nonce ensures freshness),
// but every issued token verifies against the same sessionID.
//
// sessionID may be empty for unauthenticated forms — but in that case
// the token is only useful as a per-process replay guard, not as a
// cross-session attacker shield. Prefer to bind to a stable identifier
// (session cookie value, JWT subject, anonymous ID).
func (i *Issuer) Issue(sessionID string) (Token, error) {
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

	mac := computeMAC(i.secret, sessionID, now, nonce)
	full := append(body, mac...)

	return Token(base64.RawURLEncoding.EncodeToString(full)), nil
}

// Verify returns nil on success, or one of [ErrTokenInvalid],
// [ErrTokenExpired], [ErrSessionMismatch].
//
// The HMAC compare is constant-time; the session-binding check is also
// constant-time over the prefix bytes. Callers should not branch on
// which specific sentinel was returned — log the error verbatim.
func (i *Issuer) Verify(t Token, sessionID string) error {
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

	expectedPrefix := sessionPrefix(sessionID)
	if subtle.ConstantTimeCompare(prefix, expectedPrefix) != 1 {
		return ErrSessionMismatch
	}

	expectedMAC := computeMAC(i.secret, sessionID, iat, nonce)
	if !hmac.Equal(mac, expectedMAC) {
		return ErrTokenInvalid
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

func sessionPrefix(sessionID string) []byte {
	h := sha256.Sum256([]byte(sessionID))
	return h[:sessionPrefixLen]
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
		m[strings.ToLower(strings.TrimSpace(o))] = struct{}{}
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
	// Parse scheme://host[:port] without pulling net/url for every check.
	// Anything past the host is ignored (path / query / fragment).
	raw = strings.ToLower(strings.TrimSpace(raw))
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 {
		return false
	}
	hostStart := schemeEnd + len("://")
	hostEnd := len(raw)
	for i := hostStart; i < len(raw); i++ {
		c := raw[i]
		if c == '/' || c == '?' || c == '#' {
			hostEnd = i
			break
		}
	}
	candidate := raw[:hostEnd]
	_, ok := a.origins[candidate]
	return ok
}
