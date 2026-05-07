// Package paseto wraps PASETO v4 issuance and verification with the
// kit's standard claim-validation conventions (issuer, audience,
// expiration, not-before).
//
// Compared to JWT, PASETO eliminates algorithm negotiation: every v4
// token has exactly one signing or encryption algorithm baked into the
// version+purpose tuple, removing the alg=none and key-confusion attack
// classes. Use this package for greenfield internal services where
// Oathkeeper compatibility (which mandates JWT) is not a constraint —
// security/jwtutil remains the right choice for Oathkeeper-fronted
// deployments.
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
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"aidanwoods.dev/go-paseto"
)

// Sentinel errors. Verify wraps the underlying library error in one of
// these so callers can branch without parsing strings.
var (
	ErrTokenInvalid    = errors.New("paseto: invalid token")
	ErrTokenExpired    = errors.New("paseto: token expired")
	ErrTokenNotYet     = errors.New("paseto: token not yet valid")
	ErrIssuerMismatch  = errors.New("paseto: issuer mismatch")
	ErrAudienceUnknown = errors.New("paseto: audience mismatch")
)

// Claims is the kit-canonical claim set. Mirrors jwtutil.Claims so
// downstream code can swap providers without rewriting consumers.
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
func WithClockSkewTolerance(d time.Duration) Option {
	return func(c *config) { c.clockSkewTolerance = d }
}

func buildConfig(opts []Option) (config, error) {
	cfg := config{}
	for _, o := range opts {
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
	cfg     config
	parser  paseto.Parser
	pubKeys []paseto.V4AsymmetricPublicKey
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
	for i, k := range pubKeys {
		w, kerr := paseto.NewV4AsymmetricPublicKeyFromBytes(k)
		if kerr != nil {
			return nil, fmt.Errorf("paseto: invalid Ed25519 public key %d: %w", i, kerr)
		}
		wrapped = append(wrapped, w)
	}

	return &V4Public{
		cfg:     cfg,
		parser:  paseto.Parser{}, // empty rule set; we apply exp/nbf in validate()
		pubKeys: wrapped,
	}, nil
}

// Verify parses, authenticates, and validates token's reserved claims
// against the configured issuer/audience and the supplied now.
func (v *V4Public) Verify(token string, now time.Time) (*Claims, error) {
	var (
		parsed  *paseto.Token
		lastErr error
	)
	for _, k := range v.pubKeys {
		t, err := v.parser.ParseV4Public(k, token, nil)
		if err == nil {
			parsed = t
			break
		}
		lastErr = err
	}
	if parsed == nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenInvalid, lastErr)
	}
	return v.validate(parsed, now)
}

// Sign issues a v4.public token. The privateKey must match one of the
// public keys configured on the verifier; the issuer and audience
// claims are populated from the configured defaults if omitted in
// claims.
func (v *V4Public) Sign(claims Claims, privateKey ed25519.PrivateKey) (string, error) {
	priv, err := paseto.NewV4AsymmetricSecretKeyFromBytes(privateKey)
	if err != nil {
		return "", fmt.Errorf("paseto: invalid Ed25519 private key: %w", err)
	}
	tok := buildToken(claims, v.cfg)
	return tok.V4Sign(priv, nil), nil
}

// V4Local verifies and seals v4.local tokens (XChaCha20-Poly1305).
type V4Local struct {
	cfg    config
	parser paseto.Parser
	key    paseto.V4SymmetricKey
}

// NewV4Local constructs a sealer/verifier for v4.local tokens. The key
// must be 32 bytes.
func NewV4Local(key []byte, opts ...Option) (*V4Local, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("paseto: V4Local key must be 32 bytes, got %d", len(key))
	}
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}
	wrapped, err := paseto.V4SymmetricKeyFromBytes(key)
	if err != nil {
		return nil, fmt.Errorf("paseto: invalid V4Local key: %w", err)
	}
	return &V4Local{cfg: cfg, parser: paseto.NewParser(), key: wrapped}, nil
}

// Verify parses, authenticates, and validates a v4.local token.
func (v *V4Local) Verify(token string, now time.Time) (*Claims, error) {
	parsed, err := v.parser.ParseV4Local(v.key, token, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}
	return v.validate(parsed, now)
}

// Seal issues a v4.local token under the configured key.
func (v *V4Local) Seal(claims Claims) (string, error) {
	tok := buildToken(claims, v.cfg)
	return tok.V4Encrypt(v.key, nil), nil
}

func buildToken(c Claims, cfg config) paseto.Token {
	t := paseto.NewToken()
	if c.Subject != "" {
		t.SetSubject(c.Subject)
	}
	if iss := pick(c.Issuer, cfg.expectedIssuer); iss != "" {
		t.SetIssuer(iss)
	}
	if len(c.Audience) > 0 {
		// PASETO supports a single string audience; if the caller
		// supplied multiple audiences, encode the first as the
		// canonical aud claim and the rest as a custom "aud_alt"
		// array so downstream readers can still see the full set.
		t.SetAudience(c.Audience[0])
		if len(c.Audience) > 1 {
			_ = t.Set("aud_alt", c.Audience[1:])
		}
	} else if cfg.expectedAudience != "" {
		t.SetAudience(cfg.expectedAudience)
	}
	if !c.IssuedAt.IsZero() {
		t.SetIssuedAt(c.IssuedAt)
	}
	if !c.ExpiresAt.IsZero() {
		t.SetExpiration(c.ExpiresAt)
	}
	if !c.NotBefore.IsZero() {
		t.SetNotBefore(c.NotBefore)
	}
	for k, v := range c.Custom {
		_ = t.Set(k, v)
	}
	return t
}

func (v *V4Public) validate(t *paseto.Token, now time.Time) (*Claims, error) {
	return validate(t, v.cfg, now)
}

func (v *V4Local) validate(t *paseto.Token, now time.Time) (*Claims, error) {
	return validate(t, v.cfg, now)
}

func validate(t *paseto.Token, cfg config, now time.Time) (*Claims, error) {
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
	var altAud []string
	if err := t.Get("aud_alt", &altAud); err == nil {
		c.Audience = append(c.Audience, altAud...)
	}
	if iat, err := t.GetIssuedAt(); err == nil {
		c.IssuedAt = iat
	}
	if exp, err := t.GetExpiration(); err == nil {
		c.ExpiresAt = exp
	}
	if nbf, err := t.GetNotBefore(); err == nil {
		c.NotBefore = nbf
	}

	if !cfg.allowAnyIssuer && c.Issuer != cfg.expectedIssuer {
		return nil, fmt.Errorf("%w: got %q want %q", ErrIssuerMismatch, c.Issuer, cfg.expectedIssuer)
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
			return nil, fmt.Errorf("%w: got %v want %q", ErrAudienceUnknown, c.Audience, cfg.expectedAudience)
		}
	}

	if !c.ExpiresAt.IsZero() && now.After(c.ExpiresAt.Add(cfg.clockSkewTolerance)) {
		return nil, ErrTokenExpired
	}
	if !c.NotBefore.IsZero() && now.Before(c.NotBefore.Add(-cfg.clockSkewTolerance)) {
		return nil, ErrTokenNotYet
	}

	return c, nil
}

func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
