package jwtutil

import (
	"context"
	"crypto"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// KeyRotator returns the current JWT signing key. The [SigningProvider]
// invokes it once at construction (synchronously) and again on every
// refresh tick. Implementations typically read from a KMS, a sealed
// secret, or a workload-identity-mounted file.
//
// The returned value MUST be a private key compatible with the
// SigningProvider's configured signing algorithm — *ecdsa.PrivateKey
// for ES*, *rsa.PrivateKey for RS*/PS*, ed25519.PrivateKey for EdDSA,
// etc. The provider validates compatibility once at construction; a
// later rotation that returns a wrong-shape key fails closed via the
// [WithOnSigningRefreshError] callback and the previous key is retained
// until [WithSigningMaxStale] expires.
//
// Source errors after the initial load surface through the
// [WithOnSigningRefreshError] callback — the provider keeps signing
// with the previous successful key rather than going dark on a
// transient backend blip.
type KeyRotator func(ctx context.Context) (crypto.PrivateKey, error)

// IssuedJTIRecorder records jtis emitted by [SigningProvider.Sign].
// Wire it via [WithIssuedJTIRecorder] when a verifier-side revocation
// store must be able to look up an issued jti (e.g. for "logout this
// token" semantics that target a specific jti rather than a subject).
//
// The recorder is consulted AFTER the JWT has been signed and BEFORE
// Sign returns the token to the caller. A recorder error fails the
// Sign call so the caller cannot ship a token the issuance ledger
// failed to track — silent skip would defeat the "verifier finds a
// known jti" contract. If the recorder is slow or the ledger backend
// is down, callers must surface that pressure to the consumer.
//
// The Issuer / JTI / ExpiresAt values are derived from the signed
// token's claims, not from attacker input — the SigningProvider is
// the only writer.
type IssuedJTIRecorder interface {
	RecordIssued(ctx context.Context, issuer, jti string, expiresAt time.Time) error
}

// SigningProvider issues JWTs using a rotating signing key. It mirrors
// the paseto SigningProvider's lifecycle and stale-key contract so the
// two issuance paths behave identically in production:
//
//   - Construct via [NewSigningProvider]. The constructor performs the
//     initial key load synchronously and spawns a background goroutine
//     that refreshes the key on the configured interval.
//   - Sign issues a signed JWT using the currently-loaded key. Safe for
//     concurrent use; the hot path is an atomic load.
//   - Close terminates the refresh goroutine and zeroes the cached key
//     reference so the in-memory private key becomes eligible for GC.
//
// # Why JWT issuance moved from verify-only to issuance-capable in v2.0
//
// The jwtutil package's verify-side surface was historically the kit's
// posture — services consumed tokens minted by external IdPs and the
// kit's mandate stopped at JWKS-backed verification. v2.0 keeps that
// posture by default (the Verifier remains the primary entry point)
// but adds SigningProvider so adopter services that need to issue
// short-lived service tokens, on-behalf-of tokens, or "session-scoped"
// JWTs can do so without rolling a parallel issuance path against the
// same jwx/v3 dependency the verifier already pulls in.
//
// # Key rotation semantics
//
// SigningProvider holds a single in-memory key reference. On every
// refresh tick the [KeyRotator] is invoked; on success the reference
// is swapped atomically. The PREVIOUS key reference is then released
// to the GC — SigningProvider does NOT retain a grace window of
// previous keys for issuance. This is deliberate: an issuer that
// continues minting tokens with a rotated-out key would emit
// credentials the operator has explicitly invalidated. The grace
// window lives on the VERIFIER side (kit verifiers trust both the old
// and the new public key for the verifier's overlap window); this
// SigningProvider's contract is "issue with the freshest key the
// rotator has handed us, or fail closed".
//
// Tokens already minted before rotation continue to verify until
// their natural expiry, provided the verifier-side JWKS still
// publishes the previous public key during the overlap window. Pick
// a refresh interval substantially shorter than the verifier-side
// overlap window so an issuer that misses one rotation cycle still
// produces verifier-acceptable tokens.
//
// # jti tracking
//
// Sign mints a fresh 128-bit random jti per token unless the caller
// provides Claims.ID. A caller-provided ID is honoured verbatim
// (validated only for non-emptiness); pass an opaque high-entropy
// value if you need it to survive revocation lookups. The provider
// optionally forwards the issued jti to an [IssuedJTIRecorder] (see
// [WithIssuedJTIRecorder]) so a verifier-side revocation store can
// later mark the jti revoked.
//
// # Audience semantics
//
// A single audience is pinned at construction via
// [WithExpectedAudience]. The pinned audience becomes the sole "aud"
// value on every issued JWT. This matches paseto and matches the
// kit's confused-deputy mitigation: a SigningProvider tied to one
// audience cannot accidentally mint a token a sibling service would
// accept. Multi-audience issuance requires multiple SigningProvider
// instances (one per audience) — the v2.0 contract intentionally
// rejects per-call audience overrides because they make the
// confused-deputy posture hard to audit.
type SigningProvider struct {
	src              KeyRotator
	interval         time.Duration
	alg              jwa.SignatureAlgorithm
	expectedIssuer   string
	expectedAudience string
	defaultLifetime  time.Duration
	allowAnyIssuer   bool
	allowAnyAudience bool
	recorder         IssuedJTIRecorder
	onRefreshErr     func(error)

	current               atomic.Pointer[jwk.Key]
	lastSuccessfulRefresh atomic.Int64
	closed                atomic.Bool
	stop                  chan struct{}
	done                  chan struct{}
	stopOnce              sync.Once

	rootCtx      context.Context
	rootCancel   context.CancelFunc
	fetchTimeout time.Duration
	maxStale     time.Duration
	clock        func() time.Time
}

// SigningOption configures a [SigningProvider].
type SigningOption func(*SigningProvider)

// signing-side sentinels. These mirror the paseto package's
// ErrSignerClosed / ErrProviderClosed split so callers using both
// providers can branch on error kind without per-package type
// switches.

// ErrSigningProviderClosed is returned by [SigningProvider.Sign] after
// the provider has been [SigningProvider.Close]-shut. Lets callers
// distinguish a closed provider from a transient stale-key situation
// (which surfaces as [ErrKeySetUnavailable]).
var ErrSigningProviderClosed = errors.New("jwtutil: signing provider is closed")

// ErrSigningKeyUnavailable is returned by [SigningProvider.Sign] when
// the rotator has not produced a usable key yet, or the last
// successful refresh is older than the configured max-stale window.
// Wraps [ErrKeySetUnavailable] so legacy errors.Is checks keep
// matching.
var ErrSigningKeyUnavailable = fmt.Errorf("%w: signing key not loaded or stale", ErrKeySetUnavailable)

const (
	// defaultSigningFetchTimeout caps each rotator call independently
	// of the refresh interval. 10s mirrors the paseto SigningProvider
	// default and is large enough for a slow KMS fetch while small
	// enough that Close returns within seconds.
	defaultSigningFetchTimeout = 10 * time.Second
	// defaultSigningMaxStale is the default upper bound on how long
	// Sign keeps minting with the previous successful key after
	// refreshes start failing.
	defaultSigningMaxStale = time.Hour
)

// WithSigningExpectedIssuer pins the "iss" claim that every issued
// JWT will carry. Required by default (mirrors paseto): a
// signing-side that omits issuer pinning lets a misconfigured caller
// produce tokens that any kit-side verifier accepts regardless of
// origin. Call [WithSigningAllowAnyIssuer] only when a deliberate
// federation pattern justifies omitting the claim.
func WithSigningExpectedIssuer(s string) SigningOption {
	return func(p *SigningProvider) {
		p.expectedIssuer = s
		p.allowAnyIssuer = false
	}
}

// WithSigningExpectedAudience pins the "aud" claim on every issued
// JWT. Required by default; see [SigningProvider] for the
// single-audience-per-provider rationale.
func WithSigningExpectedAudience(s string) SigningOption {
	return func(p *SigningProvider) {
		p.expectedAudience = s
		p.allowAnyAudience = false
	}
}

// WithSigningAllowAnyIssuer opts out of the issuer-pinning
// requirement. Tokens issued by such a provider always omit the "iss"
// claim; [Claims.Issuer] is ignored at Sign time (see [SigningProvider.Sign]),
// so there is no per-call override. Use only when the deployment
// topology makes issuer pinning meaningless — every other case should
// call [WithSigningExpectedIssuer] instead.
func WithSigningAllowAnyIssuer() SigningOption {
	return func(p *SigningProvider) {
		p.allowAnyIssuer = true
		p.expectedIssuer = ""
	}
}

// WithSigningAllowAnyAudience opts out of audience-pinning. Tokens
// issued by such a provider omit the "aud" claim. Use only when a
// deliberate federation pattern justifies the missing claim;
// otherwise it reopens the RFC 7519 §4.1.3 confused-deputy hazard.
func WithSigningAllowAnyAudience() SigningOption {
	return func(p *SigningProvider) {
		p.allowAnyAudience = true
		p.expectedAudience = ""
	}
}

// WithSigningDefaultLifetime sets the lifetime applied at Sign time
// when [Claims.ExpiresAt] is zero. Must be positive. The exp claim is
// always populated — issuing a JWT without exp is rejected by every
// kit-side verifier and most external IdP-fronted consumers, so the
// SigningProvider never lets a missing exp through.
func WithSigningDefaultLifetime(d time.Duration) SigningOption {
	if d <= 0 {
		panic("jwtutil: WithSigningDefaultLifetime requires a positive duration")
	}
	return func(p *SigningProvider) { p.defaultLifetime = d }
}

// WithSigningMethod selects the JWS signature algorithm. Default:
// [jwa.ES256]. Callers wiring against a JWKS-backed IdP that
// publishes RSA keys should pass [jwa.RS256] (or PS256/PS384/PS512)
// here so the issued tokens verify under the published JWKS.
//
// Symmetric algorithms (HS*) are rejected — a SigningProvider that
// emitted HS-signed tokens would couple every verifier to the same
// secret as the issuer, which is the classic alg-confusion vector
// the verifier-side already filters out at JWKS-parse time.
func WithSigningMethod(alg jwa.SignatureAlgorithm) SigningOption {
	return func(p *SigningProvider) { p.alg = alg }
}

// WithSigningMaxStale bounds how long Sign continues to use the
// previously-loaded key after refresh failures. Once exceeded, Sign
// fails closed with [ErrSigningKeyUnavailable] instead of trusting
// stale keys forever — issuing tokens with a key the operator has
// rotated out is a credential-rotation violation.
//
// Default: 1 hour. Use [WithoutSigningMaxStaleLimit] only when an
// external health gate already enforces key-source freshness.
func WithSigningMaxStale(d time.Duration) SigningOption {
	if d <= 0 {
		panic("jwtutil: WithSigningMaxStale requires a positive duration")
	}
	return func(p *SigningProvider) { p.maxStale = d }
}

// WithoutSigningMaxStaleLimit disables stale-key expiry for the
// signing-side provider. Use only when callers enforce key-source
// freshness through an external health gate.
func WithoutSigningMaxStaleLimit() SigningOption {
	return func(p *SigningProvider) { p.maxStale = 0 }
}

// WithSigningFetchTimeout overrides the per-refresh deadline. Useful
// when the upstream key source is genuinely slow.
func WithSigningFetchTimeout(d time.Duration) SigningOption {
	if d <= 0 {
		panic("jwtutil: WithSigningFetchTimeout requires a positive duration")
	}
	return func(p *SigningProvider) { p.fetchTimeout = d }
}

// WithOnSigningRefreshError installs a callback for refresh failures.
// The initial load failure surfaces via [NewSigningProvider]'s error
// return, not this callback. The provider keeps signing with the
// previous key when refreshes fail, so the callback is the only
// signal that rotation has stalled — wire it to a metric or alert.
//
// Panics if fn is nil: silently swallowing the callback would hide
// the only operator-visible signal of stalled rotation.
func WithOnSigningRefreshError(fn func(error)) SigningOption {
	if fn == nil {
		panic("jwtutil: WithOnSigningRefreshError requires a non-nil callback")
	}
	return func(p *SigningProvider) { p.onRefreshErr = fn }
}

// WithIssuedJTIRecorder forwards every issued jti to recorder.
// See [IssuedJTIRecorder] for the contract. Panics if recorder is
// nil to fail fast at wiring time.
func WithIssuedJTIRecorder(recorder IssuedJTIRecorder) SigningOption {
	if recorder == nil {
		panic("jwtutil: WithIssuedJTIRecorder requires a non-nil recorder")
	}
	return func(p *SigningProvider) { p.recorder = recorder }
}

// WithSigningRotationInterval sets how often [KeyRotator] is invoked
// to mint a fresh key. Required at construction — there is no
// default because the right interval is workload-specific: too long
// keeps a compromised key in service, too short hammers the rotator
// backend. Mirrors the rotation cadence the verifier-side JWKS
// overlap window expects.
//
// Must be positive.
func WithSigningRotationInterval(d time.Duration) SigningOption {
	if d <= 0 {
		panic("jwtutil: WithSigningRotationInterval requires a positive duration")
	}
	return func(p *SigningProvider) { p.interval = d }
}

// withSigningProviderClock overrides the time source. Test-only seam.
func withSigningProviderClock(fn func() time.Time) SigningOption {
	return func(p *SigningProvider) { p.clock = fn }
}

// NewSigningProvider performs the initial key load synchronously and
// starts a background goroutine that refreshes on the cadence
// configured by [WithSigningRotationInterval]. The initial load
// failure surfaces as the constructor's error return; no goroutine is
// started in that case.
//
// ctx scopes the initial key load — pass a request-shaped context if
// the rotator backend should observe deadlines / cancellation during
// startup. Subsequent rotation calls derive their context from the
// provider's own lifecycle, not ctx; passing context.Background() is
// the common case.
//
// Required options:
//   - [WithSigningRotationInterval] — must be > 0.
//   - Either [WithSigningExpectedIssuer] or [WithSigningAllowAnyIssuer].
//   - Either [WithSigningExpectedAudience] or [WithSigningAllowAnyAudience].
//
// Always pair construction with [SigningProvider.Close] in a defer or
// shutdown hook.
func NewSigningProvider(ctx context.Context, rotator KeyRotator, opts ...SigningOption) (*SigningProvider, error) {
	if ctx == nil {
		return nil, errors.New("jwtutil: context must not be nil")
	}
	if rotator == nil {
		return nil, errors.New("jwtutil: KeyRotator must not be nil")
	}
	rootCtx, rootCancel := context.WithCancel(context.Background())
	p := &SigningProvider{
		src:          rotator,
		alg:          jwa.ES256(),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		rootCtx:      rootCtx,
		rootCancel:   rootCancel,
		fetchTimeout: defaultSigningFetchTimeout,
		maxStale:     defaultSigningMaxStale,
		clock:        time.Now,
	}
	for _, o := range opts {
		if o == nil {
			rootCancel()
			return nil, errors.New("jwtutil: NewSigningProvider option must not be nil")
		}
		o(p)
	}
	if p.clock == nil {
		p.clock = time.Now
	}
	if p.interval <= 0 {
		rootCancel()
		return nil, errors.New("jwtutil: NewSigningProvider requires WithSigningRotationInterval")
	}
	if err := validateSigningAlg(p.alg); err != nil {
		rootCancel()
		return nil, err
	}
	if p.expectedIssuer == "" && !p.allowAnyIssuer {
		rootCancel()
		return nil, errors.New("jwtutil: NewSigningProvider requires WithSigningExpectedIssuer or the explicit WithSigningAllowAnyIssuer opt-out")
	}
	if p.expectedAudience == "" && !p.allowAnyAudience {
		rootCancel()
		return nil, errors.New("jwtutil: NewSigningProvider requires WithSigningExpectedAudience or the explicit WithSigningAllowAnyAudience opt-out (RFC 7519 confused-deputy mitigation)")
	}

	if err := p.refresh(ctx); err != nil {
		rootCancel()
		return nil, fmt.Errorf("jwtutil: initial signing key load: %w", err)
	}

	go p.loop()
	return p, nil
}

// validateSigningAlg rejects symmetric algorithms and the no-signature
// "none" placeholder. Symmetric algorithms (HS256 and any custom alg
// registered with [jwa.WithIsSymmetric]) would couple every verifier
// to the issuer's secret (alg-confusion vector); "none" disables
// authentication outright. We consult [jwa.SignatureAlgorithm.IsSymmetric]
// rather than hardcoding the HS* name list so a future custom symmetric
// algorithm registered via [jwa.RegisterSignatureAlgorithm] is still
// rejected.
func validateSigningAlg(alg jwa.SignatureAlgorithm) error {
	name := alg.String()
	if name == "" {
		return errors.New("jwtutil: signing algorithm must not be empty")
	}
	if name == "none" {
		return errors.New("jwtutil: signing algorithm \"none\" is not permitted")
	}
	registered, ok := jwa.LookupSignatureAlgorithm(name)
	if !ok {
		return fmt.Errorf("jwtutil: unknown signing algorithm %q", name)
	}
	if registered.IsSymmetric() {
		return fmt.Errorf("jwtutil: symmetric signing algorithm %q is not permitted; use an asymmetric algorithm (ES*, RS*, PS*, EdDSA)", name)
	}
	return nil
}

// keyTypeForSigningAlg maps an asymmetric JWS signature algorithm to the
// JWK key type (kty) its private key must carry: ES*/ES256K → EC,
// RS*/PS* → RSA, EdDSA* → OKP. It returns [jwa.InvalidKeyType] for any
// algorithm whose required key type the kit cannot derive (a future
// custom algorithm registered via [jwa.RegisterSignatureAlgorithm]); the
// caller treats that as "no shape constraint" so the guard never blocks a
// key it cannot reason about. Symmetric algorithms are rejected earlier
// by [validateSigningAlg], so oct is intentionally not mapped here.
func keyTypeForSigningAlg(alg jwa.SignatureAlgorithm) jwa.KeyType {
	switch name := alg.String(); {
	case strings.HasPrefix(name, "ES"):
		// ES256/ES384/ES512/ES256K all use EC private keys.
		return jwa.EC()
	case strings.HasPrefix(name, "RS"), strings.HasPrefix(name, "PS"):
		// RS256/384/512 and PS256/384/512 all use RSA private keys.
		return jwa.RSA()
	case strings.HasPrefix(name, "EdDSA"):
		// EdDSA / EdDSAEd25519 / EdDSAEd448 all use OKP private keys.
		return jwa.OKP()
	default:
		return jwa.InvalidKeyType()
	}
}

// Sign issues a JWT using the currently-loaded key. Returns
// [ErrSigningProviderClosed] after [SigningProvider.Close],
// [ErrSigningKeyUnavailable] when the key has expired its
// [WithSigningMaxStale] window or never loaded, and the recorder's
// error if [WithIssuedJTIRecorder] is wired and the ledger write
// fails.
//
// The claims argument is interpreted as follows:
//   - Subject must be non-empty.
//   - ID is used verbatim if set; otherwise a 128-bit random jti is
//     minted.
//   - IssuedAt defaults to the SigningProvider's clock when zero.
//   - ExpiresAt defaults to IssuedAt + WithSigningDefaultLifetime
//     when zero; if no default lifetime is configured and the caller
//     omits ExpiresAt, Sign returns an error rather than minting a
//     non-expiring token.
//   - Issuer and audience are taken from the pinned values; caller
//     values in Claims.Issuer are ignored to prevent per-call
//     overrides from defeating the confused-deputy mitigation.
//   - Permissions and Scopes are emitted as custom claims when
//     non-empty; absent fields keep the standard wire shape.
func (p *SigningProvider) Sign(claims Claims) (string, error) {
	return p.SignContext(context.Background(), claims)
}

// SignContext is the context-aware variant of [SigningProvider.Sign].
// The context is forwarded to the [IssuedJTIRecorder] when one is
// wired so cache backends can observe request cancellation. Token
// construction itself is not cancellable — jwx's signer is in-memory
// and synchronous — so a cancelled context here only short-circuits
// the recorder hop.
func (p *SigningProvider) SignContext(ctx context.Context, claims Claims) (string, error) {
	if p == nil {
		return "", ErrSigningKeyUnavailable
	}
	if p.closed.Load() {
		return "", ErrSigningProviderClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	keyPtr := p.current.Load()
	if keyPtr == nil {
		// Re-check the closed flag: Close stores closed=true before it
		// nils current, so a Sign that races Close should report
		// ErrSigningProviderClosed rather than ErrSigningKeyUnavailable.
		// Go's atomic Load establishes the happens-before edge we need.
		if p.closed.Load() {
			return "", ErrSigningProviderClosed
		}
		return "", ErrSigningKeyUnavailable
	}
	if p.maxStale > 0 {
		last := p.lastSuccessfulRefresh.Load()
		if last == 0 {
			return "", ErrSigningKeyUnavailable
		}
		if p.clock().Sub(time.Unix(0, last)) > p.maxStale {
			return "", ErrSigningKeyUnavailable
		}
	}
	if claims.Subject == "" {
		return "", errors.New("jwtutil: Claims.Subject must not be empty")
	}

	now := p.clock()
	issuedAt := now
	if claims.IssuedAt > 0 {
		issuedAt = time.Unix(claims.IssuedAt, 0)
	}
	var expiresAt time.Time
	switch {
	case claims.ExpiresAt > 0:
		expiresAt = time.Unix(claims.ExpiresAt, 0)
	case p.defaultLifetime > 0:
		expiresAt = issuedAt.Add(p.defaultLifetime)
	default:
		return "", errors.New("jwtutil: Claims.ExpiresAt is required when no default lifetime is configured")
	}
	if !expiresAt.After(issuedAt) {
		return "", errors.New("jwtutil: Claims.ExpiresAt must be after IssuedAt")
	}

	jti := claims.ID
	if jti == "" {
		var err error
		jti, err = newRandomJTI()
		if err != nil {
			return "", fmt.Errorf("jwtutil: mint jti: %w", err)
		}
	}

	builder := jwt.NewBuilder().
		Subject(claims.Subject).
		IssuedAt(issuedAt).
		Expiration(expiresAt).
		JwtID(jti)
	if !p.allowAnyIssuer && p.expectedIssuer != "" {
		builder = builder.Issuer(p.expectedIssuer)
	}
	if !p.allowAnyAudience && p.expectedAudience != "" {
		builder = builder.Audience([]string{p.expectedAudience})
	}
	if claims.NotBefore > 0 {
		builder = builder.NotBefore(time.Unix(claims.NotBefore, 0))
	}
	tok, err := builder.Build()
	if err != nil {
		return "", fmt.Errorf("jwtutil: build token: %w", err)
	}
	if len(claims.Permissions) > 0 {
		if err := tok.Set("permissions", claims.Permissions); err != nil {
			return "", fmt.Errorf("jwtutil: set permissions claim: %w", err)
		}
	}
	if claims.Scopes != "" {
		if err := tok.Set("scopes", claims.Scopes); err != nil {
			return "", fmt.Errorf("jwtutil: set scopes claim: %w", err)
		}
	}

	signed, err := jwt.Sign(tok, jwt.WithKey(p.alg, *keyPtr))
	if err != nil {
		return "", fmt.Errorf("jwtutil: sign token: %w", err)
	}

	if p.recorder != nil {
		issuer := p.expectedIssuer
		if err := p.recorder.RecordIssued(ctx, issuer, jti, expiresAt); err != nil {
			return "", fmt.Errorf("jwtutil: record issued jti: %w", err)
		}
	}

	return string(signed), nil
}

// newRandomJTI returns a 128-bit hex-encoded random value. 128 bits is
// well above the collision floor for any realistic issuance rate and
// fits within the de-facto 256-character jti budget observed in
// federation-tested issuers.
func newRandomJTI() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// Close terminates the refresh goroutine and releases the cached key
// reference. Subsequent Sign calls return [ErrSigningProviderClosed].
// Idempotent; safe for concurrent use. Always returns nil — the
// signature matches [io.Closer] so the provider can be wired into
// resource-cleanup helpers, but the shutdown path itself cannot fail.
func (p *SigningProvider) Close() error {
	if p == nil || p.stop == nil || p.done == nil {
		return nil
	}
	p.stopOnce.Do(func() {
		p.closed.Store(true)
		close(p.stop)
		if p.rootCancel != nil {
			p.rootCancel()
		}
	})
	<-p.done
	// Drop the cached key reference so the underlying private key
	// becomes eligible for GC. We cannot zero the bytes in place
	// here: jwk.Key wraps the standard-library crypto key types
	// (*ecdsa.PrivateKey, *rsa.PrivateKey, ed25519.PrivateKey) whose
	// internal layout we do not own. Operators who need
	// guaranteed-zero key material at shutdown should run inside a
	// memory-locked process and rely on the KMS to issue ephemeral
	// keys via KeyRotator instead.
	p.current.Store(nil)
	return nil
}

func (p *SigningProvider) loop() {
	defer close(p.done)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			// Derive each refresh from rootCtx (cancelled by Close)
			// so an in-flight Close aborts the rotator call instead
			// of waiting for the per-refresh timeout. The per-refresh
			// timeout uses fetchTimeout — independent of p.interval —
			// so a long polling cadence does not translate into a
			// long shutdown delay.
			ctx, cancel := context.WithTimeout(p.rootCtx, p.fetchTimeout)
			err := p.refresh(ctx)
			cancel()
			if err != nil {
				p.callOnRefreshError(err)
			}
		}
	}
}

func (p *SigningProvider) callOnRefreshError(err error) {
	if p.onRefreshErr == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("jwtutil: OnSigningRefreshError callback panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
		}
	}()
	p.onRefreshErr(err)
}

func (p *SigningProvider) refresh(ctx context.Context) error {
	raw, err := p.src(ctx)
	if err != nil {
		return err
	}
	if raw == nil {
		return errors.New("jwtutil: KeyRotator returned a nil key")
	}
	key, err := jwk.Import(raw)
	if err != nil {
		return fmt.Errorf("jwtutil: import signing key: %w", err)
	}
	// Reject symmetric (oct) key material at the rotator boundary so a
	// caller that mistakenly passes a []byte (which jwk.Import wraps as
	// a symmetric key) cannot land here. The constructor already
	// rejects symmetric algorithms; this second guard closes the
	// matching alg-confusion vector on the key side.
	if _, isSymmetric := key.(jwk.SymmetricKey); isSymmetric {
		return errors.New("jwtutil: KeyRotator returned a symmetric key; SigningProvider requires an asymmetric private key")
	}
	// Reject a wrong-shape key (e.g. an RSA key handed to an ES256
	// provider after a KMS misconfiguration). jwk.Import happily wraps
	// any private key regardless of p.alg, and Set(AlgorithmKey, p.alg)
	// merely tags the JWK with a mismatched "alg" header — both succeed,
	// so without this guard refresh would return nil, fire no
	// OnSigningRefreshError callback, update lastSuccessfulRefresh, and
	// overwrite the previously-good key. Every later Sign would then fail
	// forever (jwt.Sign rejects the kty/alg mismatch) while maxStale
	// never triggers because the refresh "succeeded". Fail closed here so
	// the callback fires and the prior key is retained.
	if want := keyTypeForSigningAlg(p.alg); want != jwa.InvalidKeyType() && key.KeyType() != want {
		return fmt.Errorf("jwtutil: KeyRotator returned a %s key, which is incompatible with signing algorithm %q (expected a %s key)", key.KeyType(), p.alg, want)
	}
	if err := key.Set(jwk.AlgorithmKey, p.alg); err != nil {
		return fmt.Errorf("jwtutil: tag signing key algorithm: %w", err)
	}
	// Tag the imported key with an RFC 7638 thumbprint as its kid. The
	// rotator returns a stdlib [crypto.PrivateKey], which carries no kid
	// of its own, so AssignKeyID always writes here — but it is guarded
	// with hasKID for forward compatibility in case the rotator interface
	// ever returns a pre-populated jwk.Key. The verifier-side JWKS lookup
	// pivots on kid, and a kit-shipped JWKS endpoint (or a manually
	// published rotation pair) computes the same RFC 7638 thumbprint over
	// the public key — so issuer-side thumbprint kids and verifier-side
	// JWKS kids agree by construction.
	if _, hasKID := key.KeyID(); !hasKID {
		if err := jwk.AssignKeyID(key); err != nil {
			return fmt.Errorf("jwtutil: assign signing key kid: %w", err)
		}
	}
	p.current.Store(&key)
	p.lastSuccessfulRefresh.Store(p.clock().UnixNano())
	return nil
}
