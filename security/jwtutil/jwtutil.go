// Package jwtutil provides JWT verification for Oathkeeper-signed id_tokens
// backed by lestrrat-go/jwx/v3.
//
// It supports ES256 tokens signed by Oathkeeper's id_token mutator. Public
// keys are fetched from Oathkeeper's JWKS endpoint and cached with periodic
// background refresh.
package jwtutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/bds421/rho-kit/resilience/retry"
)

// uuidPattern is the canonical UUID matcher shared by httpx and grpcx auth
// middleware so the identity contract ("subject must be a UUID") cannot
// drift between transports.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s is a syntactically-valid UUID. Centralised here
// so HTTP and gRPC auth paths apply the same rule to JWT subjects and the
// X-User-Id metadata/header used by mTLS S2S impersonation.
func IsUUID(s string) bool {
	return uuidPattern.MatchString(s)
}

const (
	clockSkew              = 30 * time.Second
	defaultRefreshInterval = 10 * time.Minute
	defaultHTTPTimeout     = 5 * time.Second
	defaultMaxStale        = 1 * time.Hour
)

// Claims represents the verified JWT payload from an Oathkeeper id_token.
type Claims struct {
	Subject     string   `json:"sub"`
	Permissions []string `json:"permissions"`
	Scopes      string   `json:"scopes"`
	IssuedAt    int64    `json:"iat"`
	ExpiresAt   int64    `json:"exp"`
	NotBefore   int64    `json:"nbf"`
	Issuer      string   `json:"iss"`
}

// KeySet holds a JWKS key set for JWT signature verification.
type KeySet struct {
	set jwk.Set
	// ExpectedIssuer, when non-empty, is validated against the "iss" claim.
	ExpectedIssuer string
	// ExpectedAudience, when non-empty, is validated against the "aud" claim.
	// REQUIRED for multi-service deployments — without it, a token issued for
	// service A is silently valid at service B as long as both trust the same
	// signer. Standard JWT confused-deputy mitigation (RFC 7519 §4.1.3).
	ExpectedAudience string
}

// ParseKeySet parses a JWKS JSON document into a KeySet.
func ParseKeySet(data []byte) (*KeySet, error) {
	set, err := jwk.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse jwks: %w", err)
	}
	if set.Len() == 0 {
		return nil, errors.New("jwks contains no usable keys")
	}
	return &KeySet{set: set}, nil
}

// ParseKeySetFromPEM parses a PEM-encoded public key into a KeySet with a
// single key using the given key ID.
func ParseKeySetFromPEM(pemData []byte, kid string) (*KeySet, error) {
	key, err := jwk.ParseKey(pemData, jwk.WithPEM(true))
	if err != nil {
		return nil, fmt.Errorf("parse PEM key: %w", err)
	}
	if err := key.Set(jwk.KeyIDKey, kid); err != nil {
		return nil, err
	}
	set := jwk.NewSet()
	if err := set.AddKey(key); err != nil {
		return nil, err
	}
	return &KeySet{set: set}, nil
}

// Verify parses and verifies a compact-serialized JWT (header.payload.signature).
// It validates the signature, expiration, and not-before claims. Tokens
// without an `exp` claim are rejected — non-expiring bearer tokens are
// indistinguishable from a stolen credential and have no place in this kit.
func (ks *KeySet) Verify(tokenString string, now time.Time) (*Claims, error) {
	parseOpts := []jwt.ParseOption{
		jwt.WithKeySet(ks.set, jws.WithInferAlgorithmFromKey(true)),
		jwt.WithValidate(true),
		jwt.WithAcceptableSkew(clockSkew),
		jwt.WithClock(jwt.ClockFunc(func() time.Time { return now })),
		jwt.WithRequiredClaim(jwt.ExpirationKey),
	}
	if ks.ExpectedIssuer != "" {
		parseOpts = append(parseOpts, jwt.WithIssuer(ks.ExpectedIssuer))
	}
	if ks.ExpectedAudience != "" {
		parseOpts = append(parseOpts, jwt.WithAudience(ks.ExpectedAudience))
	}
	tok, err := jwt.Parse([]byte(tokenString), parseOpts...)
	if err != nil {
		return nil, err
	}

	exp, hasExp := tok.Expiration()
	if !hasExp || exp.IsZero() {
		// Belt-and-braces: WithRequiredClaim already enforces this, but
		// re-check after parse so a future jwx upgrade that loosens the
		// validator cannot silently re-introduce non-expiring tokens.
		return nil, errors.New("missing exp claim")
	}

	sub, _ := tok.Subject()
	if sub == "" {
		return nil, errors.New("missing sub claim")
	}

	iss, _ := tok.Issuer()
	claims := &Claims{
		Subject:   sub,
		Issuer:    iss,
		ExpiresAt: exp.Unix(),
	}
	if iat, ok := tok.IssuedAt(); ok {
		claims.IssuedAt = iat.Unix()
	}
	if nbf, ok := tok.NotBefore(); ok {
		claims.NotBefore = nbf.Unix()
	}

	var perms []any
	switch err := tok.Get("permissions", &perms); {
	case err == nil:
		converted, convErr := toStringSlice(perms)
		if convErr != nil {
			return nil, fmt.Errorf("malformed permissions claim: %w", convErr)
		}
		claims.Permissions = converted
	case errors.Is(err, jwt.ClaimNotFoundError()):
		// Older issuers and role-less tokens omit permissions entirely.
		// That is a valid token; downstream RBAC fails closed on the empty set.
	default:
		// Claim is present but not assignable to []any — e.g. a bare string
		// or number. Treating that as "no permissions" lets a buggy issuer
		// silently downgrade an authenticated request to no privileges; the
		// confused-deputy variant of the empty-set problem. Reject instead.
		slog.Warn("jwt: permissions claim malformed; rejecting token",
			"claim", "permissions",
			"err", err,
		)
		return nil, fmt.Errorf("malformed permissions claim: %w", err)
	}
	var scopes string
	switch err := tok.Get("scopes", &scopes); {
	case err == nil:
		claims.Scopes = scopes
	case errors.Is(err, jwt.ClaimNotFoundError()):
		// Optional claim; empty string is the correct zero value.
	default:
		slog.Warn("jwt: scopes claim malformed; rejecting token",
			"claim", "scopes",
			"err", err,
		)
		return nil, fmt.Errorf("malformed scopes claim: %w", err)
	}

	return claims, nil
}

// toStringSlice converts a JSON-decoded value to []string. Returns an error
// when v is the wrong shape (e.g. []any{123}) so callers can distinguish a
// misshaped claim from an empty-but-well-formed one.
func toStringSlice(v any) ([]string, error) {
	switch val := v.(type) {
	case []string:
		return val, nil
	case []any:
		out := make([]string, 0, len(val))
		for i, item := range val {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is %T, want string", i, item)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("value is %T, want []string", v)
	}
}

// Provider manages JWKS fetching and caching. It fetches the public key set
// from the configured URL on first use and refreshes it periodically.
//
// Note: jwk.Cache exists but uses the same interval for retry and refresh,
// making it unsuitable here — we need aggressive retry on startup (2s backoff)
// but infrequent periodic refresh (5–10 min).
type Provider struct {
	url              string
	httpClient       *http.Client
	refresh          time.Duration
	expectedIssuer   string
	expectedAudience string
	allowAnyIssuer   bool
	allowAnyAudience bool
	maxStale         time.Duration
	clock            func() time.Time

	mu                  sync.RWMutex
	keyset              *KeySet
	lastSuccessfulFetch time.Time
}

// ProviderOption configures optional Provider behaviour.
type ProviderOption func(*Provider)

// WithExpectedIssuer sets the JWT issuer claim that Verify will validate.
// An empty string is rejected — call [WithAllowAnyIssuer] to opt out.
func WithExpectedIssuer(issuer string) ProviderOption {
	return func(p *Provider) {
		p.expectedIssuer = issuer
		p.allowAnyIssuer = false
	}
}

// WithExpectedAudience sets the JWT audience claim that Verify will validate.
// This is the standard mitigation against the confused-deputy attack: without
// it, any service that trusts the same JWKS will accept tokens issued for any
// other service. Set this to the canonical URL or identifier of the service
// processing the token. An empty string is rejected — call
// [WithAllowAnyAudience] to opt out.
func WithExpectedAudience(audience string) ProviderOption {
	return func(p *Provider) {
		p.expectedAudience = audience
		p.allowAnyAudience = false
	}
}

// WithAllowAnyIssuer opts into the unsafe behaviour of accepting tokens
// issued by any authority. Use ONLY when a service genuinely federates
// across many issuers — even then, prefer accepting a known set with a
// custom predicate at the application layer rather than turning issuer
// validation off wholesale.
//
// At the kit's application layer ([app.Builder]), the always-on validator
// rejects [app.Builder.WithJWT] without a paired [app.Builder.WithJWTIssuer]
// or the explicit [app.Builder.WithoutJWTIssuer]; this option lets a
// hand-constructed Provider mirror that explicit opt-out.
func WithAllowAnyIssuer() ProviderOption {
	return func(p *Provider) {
		p.allowAnyIssuer = true
		p.expectedIssuer = ""
	}
}

// WithAllowAnyAudience opts into the unsafe behaviour of accepting tokens
// issued for any audience. Use ONLY when a service genuinely accepts tokens
// minted for sibling services — that is the confused-deputy hazard the
// audience claim exists to prevent (RFC 7519 §4.1.3).
func WithAllowAnyAudience() ProviderOption {
	return func(p *Provider) {
		p.allowAnyAudience = true
		p.expectedAudience = ""
	}
}

// WithMaxStale sets how long [Provider.KeySet] continues to serve a
// previously-fetched key set after a JWKS refresh failure. Once exceeded,
// KeySet returns nil and downstream verifiers fail the request closed
// rather than verifying with stale (possibly compromised) keys.
//
// The natural failure mode without this knob is "JWKS endpoint goes down,
// rotation happens, kit keeps verifying with old keys forever AND rejects
// every new token". A 1-hour default keeps short blips invisible while
// surfacing a permanent outage long before key rotation completes.
//
// Pass d <= 0 to disable max-stale (cached keys served indefinitely; the
// pre-Wave-7 behaviour). Default: 1 hour.
func WithMaxStale(d time.Duration) ProviderOption {
	return func(p *Provider) { p.maxStale = d }
}

// withClock overrides the time source. Test-only.
func withClock(fn func() time.Time) ProviderOption {
	return func(p *Provider) { p.clock = fn }
}

// NewProvider creates a JWKS provider that fetches public keys from the given URL.
// The refresh interval controls how often keys are re-fetched in the background.
// If httpClient is nil, a default client with a 5s timeout is used.
// If refresh <= 0, it defaults to 10 minutes.
//
// Issuer and audience enforcement are required by default: NewProvider panics
// unless either [WithExpectedIssuer] or [WithAllowAnyIssuer] is supplied, and
// likewise for the audience pair. Without those guardrails any correctly-signed
// token from the JWKS authority verifies for any issuer or audience — the
// classic confused-deputy hazard (RFC 7519 §4.1.3).
//
// The kit's [app.Builder] enforces the same pairing at startup via the
// always-on production-safety validator; standalone callers must opt in
// explicitly when federation across issuers/audiences is the intended design.
func NewProvider(url string, httpClient *http.Client, refresh time.Duration, opts ...ProviderOption) *Provider {
	if httpClient == nil {
		httpClient = defaultHTTPClient()
	}
	if refresh <= 0 {
		refresh = defaultRefreshInterval
	}
	p := &Provider{
		url:        url,
		httpClient: httpClient,
		refresh:    refresh,
		maxStale:   defaultMaxStale,
		clock:      time.Now,
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.clock == nil {
		p.clock = time.Now
	}
	if p.expectedIssuer == "" && !p.allowAnyIssuer {
		panic("jwtutil: NewProvider requires WithExpectedIssuer or the explicit WithAllowAnyIssuer opt-out")
	}
	if p.expectedAudience == "" && !p.allowAnyAudience {
		panic("jwtutil: NewProvider requires WithExpectedAudience or the explicit WithAllowAnyAudience opt-out (RFC 7519 confused-deputy mitigation)")
	}
	return p
}

// NewProviderWithKeySet creates a Provider pre-loaded with a key set.
// This is intended for testing — the provider will not fetch or refresh keys.
//
// max-stale is implicitly disabled because there is no fetch loop to
// refresh the lastSuccessfulFetch timestamp; staleness is meaningless when
// the keys are pinned by hand.
//
// Issuer and audience enforcement match [NewProvider]: the constructor
// panics unless either [WithExpectedIssuer] or [WithAllowAnyIssuer] is
// supplied, and likewise for the audience pair. A pinned-keyset provider
// that skipped this check would still verify any correctly-signed token
// regardless of issuer/audience and reopen the confused-deputy hazard
// (RFC 7519 §4.1.3) the [NewProvider] guardrail closes.
//
// The supplied options also overwrite [KeySet.ExpectedIssuer] and
// [KeySet.ExpectedAudience] so the provider's policy is the source of
// truth, regardless of what was set on the keyset literal.
func NewProviderWithKeySet(ks *KeySet, opts ...ProviderOption) *Provider {
	p := &Provider{keyset: ks, clock: time.Now}
	for _, opt := range opts {
		opt(p)
	}
	if p.clock == nil {
		p.clock = time.Now
	}
	if p.expectedIssuer == "" && !p.allowAnyIssuer {
		panic("jwtutil: NewProviderWithKeySet requires WithExpectedIssuer or the explicit WithAllowAnyIssuer opt-out")
	}
	if p.expectedAudience == "" && !p.allowAnyAudience {
		panic("jwtutil: NewProviderWithKeySet requires WithExpectedAudience or the explicit WithAllowAnyAudience opt-out (RFC 7519 confused-deputy mitigation)")
	}
	if ks != nil {
		ks.ExpectedIssuer = p.expectedIssuer
		ks.ExpectedAudience = p.expectedAudience
	}
	return p
}

// Run starts the background JWKS refresh loop. It blocks until ctx is cancelled.
// Call this in a goroutine before serving requests.
func (p *Provider) Run(ctx context.Context) {
	if p.url == "" {
		// No JWKS URL configured (e.g. test provider created via NewProviderWithKeySet).
		// Block until context is cancelled to match the expected lifecycle contract.
		<-ctx.Done()
		return
	}

	// Phase 1: initial fetch with retry until success.
	err := retry.Do(ctx, func(ctx context.Context) error {
		return p.fetch(ctx)
	},
		retry.WithMaxRetries(-1),
		retry.WithBaseDelay(2*time.Second),
		retry.WithMaxDelay(60*time.Second),
		retry.WithFactor(2.0),
		retry.WithJitter(0.25),
	)
	if err != nil {
		return // context cancelled
	}

	// Phase 2: periodic refresh — failures are non-fatal (cached keys remain valid).
	ticker := time.NewTicker(p.refresh)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.fetch(ctx); err != nil {
				slog.Warn("jwks periodic refresh failed, using cached keys",
					"url", p.url, "error", err)
			}
		}
	}
}

func defaultHTTPClient() *http.Client {
	// Clone http.DefaultTransport so we keep its proxy handling, dialer
	// timeouts, TLS handshake timeout, idle-conn pool, and HTTP/2 attempt
	// — replacing it wholesale loses every one of those production
	// defaults. We only tighten one knob:
	//
	// MaxResponseHeaderBytes caps the JWKS response header size at 64 KB.
	// The Go default of 0 means "1 MB", plenty for a real JWKS service
	// but enough room for a hostile JWKS endpoint to ship pathological
	// headers (e.g., a SET-COOKIE flood) that bloats memory under
	// attacker influence. The body cap is enforced separately at fetch
	// time (1 MB via io.LimitReader).
	//
	// Processes can replace http.DefaultTransport with a custom RoundTripper
	// (otelhttp wrappers, test doubles); falling back to a hand-rolled
	// http.Transport with the standard-library defaults keeps construction
	// panic-free in those processes.
	var clone *http.Transport
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		clone = tr.Clone()
	} else {
		clone = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}
	clone.MaxResponseHeaderBytes = 64 * 1024
	return &http.Client{
		Timeout:   defaultHTTPTimeout,
		Transport: clone,
	}
}

// KeySet returns the current cached key set. Returns nil if keys haven't
// been fetched yet (provider not started or still retrying), OR if the
// last successful fetch is older than the max-stale window (default 1h;
// override with [WithMaxStale]).
//
// Returning nil when stale is what makes max-stale load-bearing: every
// downstream verifier (httpx auth middleware, grpcx auth interceptor)
// already treats nil-keyset as "fail the request closed", so the
// staleness check participates in that contract automatically.
func (p *Provider) KeySet() *KeySet {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.keyset == nil {
		return nil
	}
	if p.maxStale > 0 && !p.lastSuccessfulFetch.IsZero() {
		if p.clock().Sub(p.lastSuccessfulFetch) > p.maxStale {
			return nil
		}
	}
	return p.keyset
}

// LastSuccessfulFetch returns the timestamp of the most recent successful
// JWKS fetch, or the zero time if no fetch has succeeded yet. Use for
// staleness alerting / health checks.
func (p *Provider) LastSuccessfulFetch() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastSuccessfulFetch
}

// Staleness returns how long ago the last successful JWKS fetch was, or
// 0 if no fetch has succeeded. A value greater than the configured
// max-stale window means [KeySet] now returns nil.
func (p *Provider) Staleness() time.Duration {
	last := p.LastSuccessfulFetch()
	if last.IsZero() {
		return 0
	}
	return p.clock().Sub(last)
}

func (p *Provider) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks endpoint returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
	if err != nil {
		return err
	}

	ks, err := ParseKeySet(body)
	if err != nil {
		return err
	}
	ks.ExpectedIssuer = p.expectedIssuer
	ks.ExpectedAudience = p.expectedAudience

	p.mu.Lock()
	p.keyset = ks
	p.lastSuccessfulFetch = p.clock()
	p.mu.Unlock()
	return nil
}
