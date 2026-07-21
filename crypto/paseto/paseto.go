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
// # Choosing a purpose
//
// Two purposes are exposed:
//
//   - V4Public (signing): Ed25519-signed, publicly verifiable. Use when
//     many services should be able to verify but only one issues. The
//     producer holds the private key (see [V4PublicSigner]); every
//     verifier needs only the public key (see [V4PublicVerifier]).
//     Pick this for cross-service auth tokens, session cookies that
//     downstream services validate, or any case where the trust
//     boundary between issuer and verifier crosses a process.
//
//   - V4Local (encryption): XChaCha20-Poly1305-encrypted, symmetric.
//     Use when the issuer and the verifier are inside the same trust
//     boundary (server-side session tokens that never leave a single
//     fleet, opaque cookies that the same service mints and reads).
//     Local tokens are confidential — the contents cannot be read
//     without the key — while public tokens carry their claims in the
//     clear. If you need confidentiality OR you control both sides,
//     V4Local is the right choice; otherwise pick V4Public.
//
// # Signer / verifier split
//
// V4Public is split into two types so that holding the private key is
// a deliberate choice: a service that only verifies tokens never
// constructs a [V4PublicSigner] and so cannot accidentally sign
// anything. Wire [V4PublicVerifier] into your authentication
// middleware, and [V4PublicSigner] only in the one process that mints
// tokens. The [Provider] type extends [V4PublicVerifier] with periodic
// JWKS-style key rotation.
package paseto

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"aidanwoods.dev/go-paseto"
)

// Sentinel errors. Verify wraps the underlying library error in one of
// these so callers can branch without parsing strings.
var (
	ErrTokenInvalid      = errors.New("paseto: invalid token")
	ErrKeySetUnavailable = errors.New("paseto: key set unavailable")
	ErrTokenExpired      = errors.New("paseto: token expired")
	ErrTokenNotYet       = errors.New("paseto: token not yet valid")
	ErrTokenNoExp        = errors.New("paseto: token missing required exp claim")
	ErrIssuerMismatch    = errors.New("paseto: issuer mismatch")
	ErrAudienceUnknown   = errors.New("paseto: audience mismatch")
	ErrReservedClaim     = errors.New("paseto: reserved claim name in Custom")
	ErrNoExpiration      = errors.New("paseto: ExpiresAt is required (use WithDefaultLifetime to derive it, or WithoutExpiration to opt out)")
	ErrMultiAudience     = errors.New("paseto: PASETO v4 supports a single audience; pass at most one Audience entry")
	ErrInvalidVerifier   = errors.New("paseto: verifier is not initialized")

	// ErrSignerClosed is returned by [V4PublicSigner.Sign] after the
	// signer has been [V4PublicSigner.Close]-zeroed.
	ErrSignerClosed = errors.New("paseto: signer is closed")

	// ErrV4LocalClosed is returned by [V4Local.Verify] and [V4Local.Seal]
	// after the v4.local sealer has been [V4Local.Close]-zeroed.
	ErrV4LocalClosed = errors.New("paseto: V4Local is closed")

	// ErrProviderClosed is returned by [Provider.Verify] after the
	// provider has been [Provider.Close]-shut. Lets callers distinguish
	// closed from transient stale-keys situations (which surface as
	// [ErrKeySetUnavailable]).
	ErrProviderClosed = errors.New("paseto: provider is closed")
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
			panic("paseto: option must not be nil")
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

// V4PublicVerifier verifies v4.public tokens (Ed25519). Construct via
// [NewV4PublicVerifier] with the trusted public keys.
type V4PublicVerifier struct {
	cfg         config
	parser      paseto.Parser
	pubKeys     []paseto.V4AsymmetricPublicKey
	initialized bool
}

// NewV4PublicVerifier constructs a verifier for v4.public tokens with
// the given trusted Ed25519 public keys. Provide at least one key;
// the parser tries each in order during Verify.
func NewV4PublicVerifier(pubKeys []ed25519.PublicKey, opts ...Option) (*V4PublicVerifier, error) {
	if len(pubKeys) == 0 {
		return nil, errors.New("paseto: at least one Ed25519 public key required")
	}
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}

	wrapped := make([]paseto.V4AsymmetricPublicKey, 0, len(pubKeys))
	for i, k := range pubKeys {
		keyCopy := append(ed25519.PublicKey(nil), k...)
		w, kerr := paseto.NewV4AsymmetricPublicKeyFromBytes(keyCopy)
		if kerr != nil {
			return nil, fmt.Errorf("paseto: invalid Ed25519 public key %d: %w", i, kerr)
		}
		wrapped = append(wrapped, w)
	}

	return &V4PublicVerifier{
		cfg:         cfg,
		parser:      paseto.Parser{},
		pubKeys:     wrapped,
		initialized: true,
	}, nil
}

// Verify parses, authenticates, and validates token's reserved claims
// against the configured issuer/audience and the supplied now.
func (v *V4PublicVerifier) Verify(token string, now time.Time) (*Claims, error) {
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

// V4PublicSigner issues v4.public tokens. Construct via
// [NewV4PublicSigner] with the Ed25519 private key. Issuance and
// verification are split into distinct types so a service that only
// verifies tokens cannot accidentally come to hold the private key.
//
// Call [V4PublicSigner.Close] at process shutdown to overwrite the
// underlying Ed25519 private-key bytes; subsequent [V4PublicSigner.Sign]
// calls return [ErrSignerClosed].
type V4PublicSigner struct {
	cfg config
	// keyMu guards reads of priv (in Sign) against the in-place
	// zero-write performed by Close. Sign takes it for reading and Close
	// for writing, so a Close can never zero the Ed25519 backing array
	// while ed25519.Sign is reading it. The atomic closed flag is kept
	// for the lock-free already-closed fast path.
	keyMu       sync.RWMutex
	priv        paseto.V4AsymmetricSecretKey
	initialized bool
	closed      atomic.Bool
}

// NewV4PublicSigner constructs a signer for v4.public tokens. The
// privateKey is copied so the caller may zero its slice afterwards.
func NewV4PublicSigner(privateKey ed25519.PrivateKey, opts ...Option) (*V4PublicSigner, error) {
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}
	keyCopy := append(ed25519.PrivateKey(nil), privateKey...)
	priv, err := paseto.NewV4AsymmetricSecretKeyFromBytes(keyCopy)
	if err != nil {
		return nil, fmt.Errorf("paseto: invalid Ed25519 private key: %w", err)
	}
	return &V4PublicSigner{cfg: cfg, priv: priv, initialized: true}, nil
}

// Sign issues a v4.public token. The issuer and audience claims are
// populated from the configured defaults if omitted in claims.
func (s *V4PublicSigner) Sign(claims Claims) (string, error) {
	if s == nil || !s.initialized {
		return "", ErrInvalidVerifier
	}
	// Lock-free fast path for an already-closed signer.
	if s.closed.Load() {
		return "", ErrSignerClosed
	}
	tok, err := buildToken(claims, s.cfg)
	if err != nil {
		return "", err
	}
	// Hold the read lock across the key read so Close cannot zero the
	// Ed25519 backing array mid-signature. Re-check closed under the lock:
	// Close sets the flag before acquiring the write lock, so a Close that
	// began after the fast-path check is observed here and we bail out
	// before reading (now-zeroed) key material.
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	if s.closed.Load() {
		return "", ErrSignerClosed
	}
	return tok.V4Sign(s.priv, nil), nil
}

// Close overwrites the wrapped Ed25519 private-key bytes with zeroes.
// Subsequent [V4PublicSigner.Sign] calls return [ErrSignerClosed].
// Idempotent.
//
// The upstream `aidanwoods.dev/go-paseto` library does not expose a
// zeroize/release entry, but its [paseto.V4AsymmetricSecretKey.ExportBytes]
// returns the underlying [crypto/ed25519.PrivateKey] slice without
// copying (see v4_keys.go in the upstream source). Writing zeroes
// through the returned slice therefore wipes the in-memory key
// material in place. If a future upstream release changes ExportBytes
// to defensive-copy, this method becomes a no-op for the actual
// private-key bytes — the closed flag still trips Sign — and we will
// need to update the implementation.
//
// Close is safe to call concurrently with [V4PublicSigner.Sign]: it sets
// the closed flag, then takes the write lock so the zero-write cannot
// overlap an in-flight Sign reading the same key bytes.
func (s *V4PublicSigner) Close() error {
	if s == nil {
		return nil
	}
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if !s.initialized {
		return nil
	}
	// Wait for any in-flight Sign to release its read lock before wiping
	// the shared Ed25519 backing array.
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	raw := s.priv.ExportBytes()
	for i := range raw {
		raw[i] = 0
	}
	return nil
}

// V4Local verifies and seals v4.local tokens (XChaCha20-Poly1305).
// V4Local is safe for concurrent use by multiple goroutines.
type V4Local struct {
	cfg    config
	parser paseto.Parser
	// keyMu guards reads of key against the zero/replace write performed
	// by Close, matching V4PublicSigner. Seal/Verify take RLock; Close
	// takes the write lock after setting the closed flag.
	keyMu  sync.RWMutex
	key    paseto.V4SymmetricKey
	// keyBytes is the kit-owned copy of the raw 32-byte key. We
	// retain it so [V4Local.Close] can zero a slice we own,
	// independent of whether the upstream library's
	// V4SymmetricKeyFromBytes/ExportBytes contract returns a fresh
	// copy or shares the slice. Wave 66 strengthened the Close
	// guarantee here.
	keyBytes    []byte
	initialized bool
	closed      atomic.Bool
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
	// Use a parser with no preloaded rules so this package's validate()
	// owns all exp/nbf checks, matching NewV4PublicVerifier (which uses a
	// zero-value paseto.Parser{}). paseto.NewParser() preloads the upstream
	// NotExpired rule, which compares exp to time.Now() ignoring the
	// caller-supplied now and the configured clock-skew tolerance, errors
	// when exp is missing (breaking WithoutExpiration), and surfaces expiry
	// as a generic parse error rather than ErrTokenExpired.
	return &V4Local{cfg: cfg, parser: paseto.NewParserWithoutExpiryCheck(), key: wrapped, keyBytes: keyCopy, initialized: true}, nil
}

// Verify parses, authenticates, and validates a v4.local token.
func (v *V4Local) Verify(token string, now time.Time) (*Claims, error) {
	if err := v.validateReady(); err != nil {
		return nil, err
	}
	if v.closed.Load() {
		return nil, ErrV4LocalClosed
	}
	v.keyMu.RLock()
	defer v.keyMu.RUnlock()
	if v.closed.Load() {
		return nil, ErrV4LocalClosed
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
	if v.closed.Load() {
		return "", ErrV4LocalClosed
	}
	tok, err := buildToken(claims, v.cfg)
	if err != nil {
		return "", err
	}
	v.keyMu.RLock()
	defer v.keyMu.RUnlock()
	if v.closed.Load() {
		return "", ErrV4LocalClosed
	}
	return tok.V4Encrypt(v.key, nil), nil
}

// Close overwrites the wrapped XChaCha20 symmetric-key bytes with zeroes.
// Subsequent [V4Local.Seal] / [V4Local.Verify] calls return
// [ErrV4LocalClosed]. Idempotent and safe for concurrent use.
//
// Unlike [V4PublicSigner.Close], the upstream
// [paseto.V4SymmetricKey] stores its material in a backing [32]byte
// value array, and [paseto.V4SymmetricKey.ExportBytes] has a value
// receiver — it returns a slice over a *copy* of that array, so zeroing
// the returned slice does NOT touch v.key's own material. To actually
// wipe the wrapped key we replace v.key with one rebuilt from a zero
// buffer; the previous V4SymmetricKey value (with its live material)
// becomes garbage and is reclaimed by the GC. We also zero the kit-owned
// raw copy, which we fully control.
func (v *V4Local) Close() error {
	if v == nil {
		return nil
	}
	if !v.closed.CompareAndSwap(false, true) {
		return nil
	}
	if !v.initialized {
		return nil
	}
	// Wait for in-flight Seal/Verify to release their read locks.
	v.keyMu.Lock()
	defer v.keyMu.Unlock()
	// Zero the kit-owned copy — we KNOW we own these bytes.
	for i := range v.keyBytes {
		v.keyBytes[i] = 0
	}
	// Replace the wrapped key with one derived from a zero buffer so its
	// own backing array no longer holds live key material. Constructing
	// from a 32-byte zero slice cannot fail (length is correct), but we
	// guard the error path to stay honest about the upstream contract.
	if zeroed, err := paseto.V4SymmetricKeyFromBytes(make([]byte, 32)); err == nil {
		v.key = zeroed
	}
	return nil
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
			// Do not wrap the library error: it embeds the claim key, which
			// may be a secret identifier. Surface the failing value's type
			// so wiring bugs remain diagnosable without leaking keys.
			return paseto.Token{}, fmt.Errorf("paseto: set custom claim failed: value of type %T is not serializable", v)
		}
	}
	return t, nil
}

func (v *V4PublicVerifier) validate(t *paseto.Token, now time.Time) (*Claims, error) {
	return validate(t, v.cfg, now)
}

func (v *V4Local) validate(t *paseto.Token, now time.Time) (*Claims, error) {
	return validate(t, v.cfg, now)
}

func (v *V4PublicVerifier) validateReady() error {
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
			if _, knownTop := knownTopLevelClaims[name]; knownTop {
				continue
			}
			return nil, ErrReservedClaim
		}
		c.Custom[name] = val
	}

	return c, nil
}

// knownTopLevelClaims is the subset of reservedClaims that the
// verifier itself consumes via the typed Claims fields. Anything else
// in reservedClaims (e.g. kid, aud_alt) appearing as a custom claim
// on the wire indicates the token was minted by a producer that
// bypassed the buildToken check — reject rather than silently drop.
var knownTopLevelClaims = map[string]struct{}{
	"iss": {},
	"aud": {},
	"exp": {},
	"nbf": {},
	"iat": {},
	"sub": {},
	"jti": {},
}

func verificationTime(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now()
	}
	return now
}
