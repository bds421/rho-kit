// Package paseto wraps PASETO v4 issuance and verification with the
// kit's standard claim-validation conventions (issuer, audience,
// expiration, not-before).
//
// Compared to JWT, PASETO eliminates algorithm negotiation: every v4
// token has exactly one signing or encryption algorithm baked into the
// version+purpose tuple, removing the alg=none and key-confusion attack
// classes. Use this package for greenfield internal services where JWT
// ecosystem compatibility is not a constraint. Use security/jwtutil when
// services must verify tokens from an existing JWKS/JWT issuer.
//
// Two purposes are exposed:
//
//   - V4Public: Ed25519-signed, publicly verifiable. Use when many
//     services should be able to verify but only one issues.
//   - V4Local: XChaCha20-Poly1305-encrypted, symmetric. Use when the
//     issuer and the verifier are the same trust boundary (server-side
//     session tokens, opaque cookies).
package paseto

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"aidanwoods.dev/go-paseto"
)

// Sentinel errors. Verify wraps the underlying library error in one of
// these so callers can branch without parsing strings.
var (
	ErrTokenInvalid       = errors.New("paseto: invalid token")
	ErrKeySetUnavailable  = errors.New("paseto: key set unavailable")
	ErrTokenExpired       = errors.New("paseto: token expired")
	ErrTokenNotYet        = errors.New("paseto: token not yet valid")
	ErrTokenNoExp         = errors.New("paseto: token missing required exp claim")
	ErrIssuerMismatch     = errors.New("paseto: issuer mismatch")
	ErrAudienceUnknown    = errors.New("paseto: audience mismatch")
	ErrReservedClaim      = errors.New("paseto: reserved claim name in Custom")
	ErrNoExpiration       = errors.New("paseto: ExpiresAt is required (use WithDefaultLifetime to derive it, or WithoutExpiration to opt out)")
	ErrMultiAudience      = errors.New("paseto: PASETO v4 supports a single audience; pass at most one Audience entry")
	ErrInvalidVerifier    = errors.New("paseto: verifier is not initialized")
	ErrSigningKeyMismatch = errors.New("paseto: signing key does not match configured public keys")
)

// reservedClaims are the names of standard registered claims that
// must not appear in Claims.Custom — they are owned by the typed
// fields on Claims and the verifier.
var reservedClaims = map[string]struct{}{
	"iss":     {},
	"aud":     {},
	"exp":     {},
	"nbf":     {},
	"iat":     {},
	"sub":     {},
	"jti":     {},
	"kid":     {},
	"aud_alt": {}, // legacy kit-specific name; reject to prevent revival of removed semantics.
}

// Claims is the kit-canonical claim set. Mirrors jwtutil.Claims so
// downstream code can swap providers without rewriting consumers.
//
// PASETO v4 supports a single audience string. Audience must contain
// at most one entry; if you need multi-audience semantics, issue
// separate tokens or front them with an explicit verifier policy.
type Claims struct {
	Subject   string
	Audience  []string
	Issuer    string
	IssuedAt  time.Time
	ExpiresAt time.Time
	NotBefore time.Time
	Custom    map[string]any
}

type config struct {
	expectedIssuer     string
	expectedAudience   string
	allowAnyIssuer     bool
	allowAnyAudience   bool
	clockSkewTolerance time.Duration
	requireExp         bool
	defaultLifetime    time.Duration
}

// Option configures the V4 wrappers.
type Option func(*config)

// WithExpectedIssuer requires every verified token to declare iss=s.
// Mandatory in production: federated services that don't pin an issuer
// accept any signed token, which defeats the protection PASETO offers.
func WithExpectedIssuer(s string) Option {
	return func(c *config) { c.expectedIssuer = s; c.allowAnyIssuer = false }
}

// WithExpectedAudience requires every verified token to include aud=s.
func WithExpectedAudience(s string) Option {
	return func(c *config) { c.expectedAudience = s; c.allowAnyAudience = false }
}

// WithAllowAnyIssuer opts out of issuer enforcement explicitly. Use
// only when downstream verification covers issuer authenticity by
// other means (e.g. mTLS-pinned KEK).
func WithAllowAnyIssuer() Option {
	return func(c *config) { c.allowAnyIssuer = true; c.expectedIssuer = "" }
}

// WithAllowAnyAudience opts out of audience enforcement explicitly.
func WithAllowAnyAudience() Option {
	return func(c *config) { c.allowAnyAudience = true; c.expectedAudience = "" }
}

// WithClockSkewTolerance allows clock skew of up to d when checking
// exp/nbf claims. Default: 0 (strict). Production usually wants 30s.
// Panics if d is negative — a negative tolerance silently tightens
// exp/nbf checks, which is never the caller's intent.
func WithClockSkewTolerance(d time.Duration) Option {
	if d < 0 {
		panic("paseto: WithClockSkewTolerance requires a non-negative duration")
	}
	return func(c *config) { c.clockSkewTolerance = d }
}

// WithoutExpiration disables the default requirement that every token
// declare an exp claim. SECURITY: tokens minted without exp are valid
// forever as long as issuer and audience match. Only use for
// non-production token classes (offline test fixtures, deterministic
// CLI tooling) where revocation is provided out-of-band.
func WithoutExpiration() Option {
	return func(c *config) { c.requireExp = false }
}

// WithDefaultLifetime sets the lifetime applied at sign/seal time when
// Claims.ExpiresAt is zero. Must be positive. The verifier still
// enforces exp presence by default — this option exists so issuers
// can pin a single lifetime in one place rather than threading it
// through every Sign call.
func WithDefaultLifetime(d time.Duration) Option {
	if d <= 0 {
		panic("paseto: WithDefaultLifetime requires a positive duration")
	}
	return func(c *config) { c.defaultLifetime = d }
}

func buildConfig(opts []Option) (config, error) {
	cfg := config{requireExp: true}
	for _, o := range opts {
		if o == nil {
			return cfg, errors.New("paseto: option must not be nil")
		}
		o(&cfg)
	}
	if !cfg.allowAnyIssuer && cfg.expectedIssuer == "" {
		return cfg, errors.New("paseto: either WithExpectedIssuer or WithAllowAnyIssuer is required")
	}
	if !cfg.allowAnyAudience && cfg.expectedAudience == "" {
		return cfg, errors.New("paseto: either WithExpectedAudience or WithAllowAnyAudience is required")
	}
	return cfg, nil
}

// V4Public verifies and signs v4.public tokens (Ed25519).
type V4Public struct {
	cfg         config
	parser      paseto.Parser
	pubKeys     []paseto.V4AsymmetricPublicKey
	rawPubKeys  []ed25519.PublicKey
	initialized bool
}

// NewV4Public constructs a verifier for v4.public tokens with the given
// trusted Ed25519 public keys. Provide at least one key; the parser
// tries each in order during Verify.
func NewV4Public(pubKeys []ed25519.PublicKey, opts ...Option) (*V4Public, error) {
	if len(pubKeys) == 0 {
		return nil, errors.New("paseto: at least one Ed25519 public key required")
	}
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}

	wrapped := make([]paseto.V4AsymmetricPublicKey, 0, len(pubKeys))
	rawPubKeys := make([]ed25519.PublicKey, 0, len(pubKeys))
	for i, k := range pubKeys {
		keyCopy := append(ed25519.PublicKey(nil), k...)
		w, kerr := paseto.NewV4AsymmetricPublicKeyFromBytes(keyCopy)
		if kerr != nil {
			return nil, fmt.Errorf("paseto: invalid Ed25519 public key %d: %w", i, kerr)
		}
		wrapped = append(wrapped, w)
		rawPubKeys = append(rawPubKeys, keyCopy)
	}

	return &V4Public{
		cfg:         cfg,
		parser:      paseto.Parser{},
		pubKeys:     wrapped,
		rawPubKeys:  rawPubKeys,
		initialized: true,
	}, nil
}

// Verify parses, authenticates, and validates token's reserved claims
// against the configured issuer/audience and the supplied now.
func (v *V4Public) Verify(token string, now time.Time) (*Claims, error) {
	if err := v.validateReady(); err != nil {
		return nil, err
	}
	var parsed *paseto.Token
	for _, k := range v.pubKeys {
		t, err := v.parser.ParseV4Public(k, token, nil)
		if err == nil {
			parsed = t
			break
		}
	}
	if parsed == nil {
		return nil, fmt.Errorf("%w: authentication failed", ErrTokenInvalid)
	}
	return v.validate(parsed, now)
}

// Sign issues a v4.public token. The privateKey must match one of the
// public keys configured on the verifier; the issuer and audience
// claims are populated from the configured defaults if omitted in
// claims.
func (v *V4Public) Sign(claims Claims, privateKey ed25519.PrivateKey) (string, error) {
	if err := v.validateReady(); err != nil {
		return "", err
	}
	keyCopy := append(ed25519.PrivateKey(nil), privateKey...)
	priv, err := paseto.NewV4AsymmetricSecretKeyFromBytes(keyCopy)
	if err != nil {
		return "", fmt.Errorf("paseto: invalid Ed25519 private key: %w", err)
	}
	if !v.hasSigningPublicKey(keyCopy) {
		return "", ErrSigningKeyMismatch
	}
	tok, err := buildToken(claims, v.cfg)
	if err != nil {
		return "", err
	}
	return tok.V4Sign(priv, nil), nil
}

func (v *V4Public) hasSigningPublicKey(privateKey ed25519.PrivateKey) bool {
	pub, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return false
	}
	for _, configured := range v.rawPubKeys {
		if bytes.Equal(pub, configured) {
			return true
		}
	}
	return false
}

// V4Local verifies and seals v4.local tokens (XChaCha20-Poly1305).
type V4Local struct {
	cfg         config
	parser      paseto.Parser
	key         paseto.V4SymmetricKey
	initialized bool
}

// NewV4Local constructs a sealer/verifier for v4.local tokens. The key
// must be 32 bytes.
func NewV4Local(key []byte, opts ...Option) (*V4Local, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("paseto: V4Local key must be 32 bytes")
	}
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}
	keyCopy := append([]byte(nil), key...)
	wrapped, err := paseto.V4SymmetricKeyFromBytes(keyCopy)
	if err != nil {
		return nil, fmt.Errorf("paseto: invalid V4Local key: %w", err)
	}
	return &V4Local{cfg: cfg, parser: paseto.NewParser(), key: wrapped, initialized: true}, nil
}

// Verify parses, authenticates, and validates a v4.local token.
func (v *V4Local) Verify(token string, now time.Time) (*Claims, error) {
	if err := v.validateReady(); err != nil {
		return nil, err
	}
	parsed, err := v.parser.ParseV4Local(v.key, token, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: authentication failed", ErrTokenInvalid)
	}
	return v.validate(parsed, now)
}

// Seal issues a v4.local token under the configured key.
func (v *V4Local) Seal(claims Claims) (string, error) {
	if err := v.validateReady(); err != nil {
		return "", err
	}
	tok, err := buildToken(claims, v.cfg)
	if err != nil {
		return "", err
	}
	return tok.V4Encrypt(v.key, nil), nil
}

func buildToken(c Claims, cfg config) (paseto.Token, error) {
	for k := range c.Custom {
		if _, reserved := reservedClaims[k]; reserved {
			return paseto.Token{}, ErrReservedClaim
		}
	}
	if len(c.Audience) > 1 {
		return paseto.Token{}, ErrMultiAudience
	}

	exp := c.ExpiresAt
	if exp.IsZero() && cfg.defaultLifetime > 0 {
		base := c.IssuedAt
		if base.IsZero() {
			base = time.Now()
		}
		exp = base.Add(cfg.defaultLifetime)
	}
	if exp.IsZero() && cfg.requireExp {
		return paseto.Token{}, ErrNoExpiration
	}

	t := paseto.NewToken()
	if c.Subject != "" {
		t.SetSubject(c.Subject)
	}
	if cfg.expectedIssuer != "" {
		if c.Issuer != "" && c.Issuer != cfg.expectedIssuer {
			return paseto.Token{}, ErrIssuerMismatch
		}
		t.SetIssuer(cfg.expectedIssuer)
	} else if c.Issuer != "" {
		t.SetIssuer(c.Issuer)
	}
	if cfg.expectedAudience != "" {
		if len(c.Audience) == 1 && c.Audience[0] != cfg.expectedAudience {
			return paseto.Token{}, ErrAudienceUnknown
		}
		t.SetAudience(cfg.expectedAudience)
	} else if len(c.Audience) == 1 {
		t.SetAudience(c.Audience[0])
	}
	if !c.IssuedAt.IsZero() {
		t.SetIssuedAt(c.IssuedAt)
	}
	if !exp.IsZero() {
		t.SetExpiration(exp)
	}
	if !c.NotBefore.IsZero() {
		t.SetNotBefore(c.NotBefore)
	}
	for k, v := range c.Custom {
		if err := t.Set(k, v); err != nil {
			return paseto.Token{}, errors.New("paseto: set custom claim failed")
		}
	}
	return t, nil
}

func (v *V4Public) validate(t *paseto.Token, now time.Time) (*Claims, error) {
	return validate(t, v.cfg, now)
}

func (v *V4Local) validate(t *paseto.Token, now time.Time) (*Claims, error) {
	return validate(t, v.cfg, now)
}

func (v *V4Public) validateReady() error {
	if v == nil || !v.initialized || len(v.pubKeys) == 0 {
		return ErrInvalidVerifier
	}
	return nil
}

func (v *V4Local) validateReady() error {
	if v == nil || !v.initialized {
		return ErrInvalidVerifier
	}
	return nil
}

func validate(t *paseto.Token, cfg config, now time.Time) (*Claims, error) {
	now = verificationTime(now)
	c := &Claims{Custom: make(map[string]any)}

	if sub, err := t.GetSubject(); err == nil {
		c.Subject = sub
	}
	if iss, err := t.GetIssuer(); err == nil {
		c.Issuer = iss
	}
	if aud, err := t.GetAudience(); err == nil && aud != "" {
		c.Audience = []string{aud}
	}
	if iat, err := t.GetIssuedAt(); err == nil {
		c.IssuedAt = iat
	}
	expPresent := false
	if exp, err := t.GetExpiration(); err == nil {
		c.ExpiresAt = exp
		expPresent = true
	}
	if nbf, err := t.GetNotBefore(); err == nil {
		c.NotBefore = nbf
	}

	if !cfg.allowAnyIssuer && c.Issuer != cfg.expectedIssuer {
		return nil, ErrIssuerMismatch
	}
	if !cfg.allowAnyAudience {
		match := false
		for _, a := range c.Audience {
			if a == cfg.expectedAudience {
				match = true
				break
			}
		}
		if !match {
			return nil, ErrAudienceUnknown
		}
	}

	if cfg.requireExp && !expPresent {
		return nil, ErrTokenNoExp
	}
	if expPresent && now.After(c.ExpiresAt.Add(cfg.clockSkewTolerance)) {
		return nil, ErrTokenExpired
	}
	if !c.NotBefore.IsZero() && now.Before(c.NotBefore.Add(-cfg.clockSkewTolerance)) {
		return nil, ErrTokenNotYet
	}

	for name, val := range t.Claims() {
		if _, reserved := reservedClaims[name]; reserved {
			continue
		}
		c.Custom[name] = val
	}

	return c, nil
}

func verificationTime(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now()
	}
	return now
}
