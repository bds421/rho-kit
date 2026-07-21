package jwtutil

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	neturl "net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/core/v2/config"
	"github.com/bds421/rho-kit/core/v2/tlsclone"
	"github.com/bds421/rho-kit/resilience/v2/retry"
)

// Provider manages JWKS fetching and caching. It fetches the public key set
// from the configured URL on first use and refreshes it periodically.
//
// Note: jwk.Cache exists but uses the same interval for retry and refresh,
// making it unsuitable here — we need aggressive retry on startup (2s backoff)
// but infrequent periodic refresh (5–10 min).
//
// Safe for concurrent use — Verify / VerifyContext can be called from
// many goroutines; the internal key set is swapped atomically on each
// successful refresh.
type Provider struct {
	url               string
	httpClient        *http.Client
	refresh           time.Duration
	expectedIssuer    string
	expectedAudience  string
	revocation        RevocationChecker
	extraStringClaims []string
	// stringClaimNames is the precomputed, deduplicated claim-name list
	// (defaultStringClaims ∪ extraStringClaims) used by populateStringClaims
	// so Verify does not reallocate a names slice and seen map per token.
	stringClaimNames []string
	allowAnyIssuer   bool
	allowAnyAudience bool
	allowInsecureURL bool
	maxStale         time.Duration
	clock            func() time.Time

	mu                  sync.RWMutex
	keyset              *KeySet
	lastSuccessfulFetch time.Time
	runMu               sync.Mutex
	started             bool

	// Fetch failure counters keyed by reason. Updated by fetch() and the
	// stale-rejection path in keySetWithReason. Atomic uint64 so the
	// observability collector can read them at scrape time without
	// contending on Provider.mu.
	fetchFailHTTP          atomic.Uint64
	fetchFailParse         atomic.Uint64
	fetchFailStaleRejected atomic.Uint64
	// staleRejectionCounted is set when a request first observes a stale
	// keyset and cleared on the next successful fetch, so
	// jwks_fetch_failures_total{reason="stale-rejected"} grows at
	// transition rate rather than request rate.
	staleRejectionCounted atomic.Bool
}

// JWKSFetchFailureReason classifies the cause of a JWKS fetch failure for
// metric labelling. The set is closed and bounded — new categories must
// be added explicitly so dashboards / alert rules do not silently absorb
// novel failure modes.
type jwksFetchFailureReason string

const (
	jwksFailReasonHTTP          jwksFetchFailureReason = "http"
	jwksFailReasonParse         jwksFetchFailureReason = "parse"
	jwksFailReasonStaleRejected jwksFetchFailureReason = "stale-rejected"
)

// ProviderOption configures optional Provider behaviour.
type ProviderOption func(*Provider)

// RevocationChecker reports whether a verified JWT has been revoked. Packages
// such as security/jwtutil/revocation implement this over a shared cache. The
// checker is consulted after signature, issuer, audience, and time validation,
// so it receives trusted claims rather than attacker-controlled JSON.
type RevocationChecker interface {
	IsRevoked(ctx context.Context, claims *Claims) (bool, error)
}

// ErrTokenRevoked is returned by [Provider.VerifyContext] when the configured
// revocation checker marks the verified token as revoked.
var ErrTokenRevoked = errors.New("jwtutil: token revoked")

// ErrMissingTokenID is returned when token revocation is enabled but the
// verified token has no jti claim. A revocation-enabled verifier must fail
// closed for non-revocable tokens; otherwise logout/admin-revoke semantics are
// silently bypassed for issuers that omit jti.
var ErrMissingTokenID = errors.New("jwt revocation: token jti is required")

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

// WithRevocationChecker configures Provider verification to fail closed for
// JWTs whose ID is present in a revocation store. Passing nil panics to surface
// misconfiguration at startup rather than silently disabling revocation.
func WithRevocationChecker(checker RevocationChecker) ProviderOption {
	if checker == nil {
		panic("jwtutil: WithRevocationChecker requires a non-nil checker")
	}
	return func(p *Provider) { p.revocation = checker }
}

// WithStringClaims registers additional JWT claim names to capture as
// strings during [Provider.VerifyContext]. client_id, azp, and act are
// always extracted for identity mapping in auth middleware.
func WithStringClaims(names ...string) ProviderOption {
	copied := append([]string(nil), names...)
	return func(p *Provider) {
		p.extraStringClaims = append(p.extraStringClaims, copied...)
	}
}

// WithAllowAnyIssuer opts into the unsafe behaviour of accepting tokens
// issued by any authority. Use ONLY when a service genuinely federates
// across many issuers — even then, prefer accepting a known set with a
// custom predicate at the application layer rather than turning issuer
// validation off wholesale.
//
// The app/jwt bridge module enforces the same pairing at construction:
// `jwt.Module(jwksURL)` rejects setups without `jwt.WithIssuer(...)` or
// an explicit `jwt.WithoutIssuer()`. This option lets a hand-constructed
// Provider mirror that explicit opt-out.
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
// The duration must be positive. Default: 1 hour.
func WithMaxStale(d time.Duration) ProviderOption {
	if d <= 0 {
		panic("jwtutil: WithMaxStale requires a positive duration")
	}
	return func(p *Provider) { p.maxStale = d }
}

// WithoutMaxStaleLimit disables stale-key expiry. Use only for callers that
// enforce JWKS freshness through an external health gate.
func WithoutMaxStaleLimit() ProviderOption {
	return func(p *Provider) { p.maxStale = 0 }
}

// withClock overrides the time source. Test-only.
func withClock(fn func() time.Time) ProviderOption {
	return func(p *Provider) { p.clock = fn }
}

// WithAllowInsecureURL permits a non-https JWKS URL. Required for tests
// using httptest.NewServer or for service-mesh deployments where the JWKS
// endpoint is reached over a localhost or sidecar-secured channel. Never
// use over an untrusted network — a plaintext JWKS is a token-forgery
// vector via key injection.
func WithAllowInsecureURL() ProviderOption {
	return func(p *Provider) { p.allowInsecureURL = true }
}

func validateJWKSURL(raw string, allowInsecure bool) error {
	if raw == "" {
		return nil
	}
	u, err := neturl.Parse(raw)
	if err != nil {
		return errors.New("jwtutil: NewProvider JWKS URL is invalid")
	}
	if u.Scheme == "" || u.Host == "" {
		return errors.New("jwtutil: NewProvider requires an absolute JWKS URL")
	}
	if err := config.ValidateURLHost("jwtutil: NewProvider JWKS URL", u); err != nil {
		return err
	}
	if u.User != nil {
		return errors.New("jwtutil: NewProvider JWKS URL must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("jwtutil: NewProvider JWKS URL must not contain query or fragment components")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if allowInsecure {
			return nil
		}
		return errors.New("jwtutil: NewProvider requires https:// JWKS URL (or explicit WithAllowInsecureURL opt-in)")
	default:
		return fmt.Errorf("jwtutil: NewProvider JWKS URL scheme must be https, or http with WithAllowInsecureURL")
	}
}

// isJSONContentType reports whether ct (a Content-Type header value)
// designates a JSON-family payload. Accepts the JWKS-specific
// application/jwk-set+json as well as plain application/json. Strips
// charset / boundary parameters via mime.ParseMediaType.
func isJSONContentType(ct string) bool {
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return strings.EqualFold(mediaType, "application/json") ||
		strings.EqualFold(mediaType, "application/jwk-set+json")
}

// NewProvider creates a JWKS provider that fetches public keys from the given URL.
// The refresh interval controls how often keys are re-fetched in the background.
// If httpClient is nil, a default client with a 5s timeout is used. If
// httpClient has no timeout, no transport, or no redirect policy, NewProvider
// shallow-copies it and fills the missing safety defaults so JWKS fetches stay
// bounded and pinned to the configured signer endpoint.
// If refresh <= 0, it defaults to 10 minutes.
//
// Issuer and audience enforcement are required by default: NewProvider panics
// unless either [WithExpectedIssuer] or [WithAllowAnyIssuer] is supplied, and
// likewise for the audience pair. Without those guardrails any correctly-signed
// token from the JWKS authority verifies for any issuer or audience — the
// classic confused-deputy hazard (RFC 7519 §4.1.3).
//
// The app/jwt bridge module enforces the same pairing at construction;
// standalone callers must opt in explicitly when federation across
// issuers/audiences is the intended design.
func NewProvider(url string, httpClient *http.Client, refresh time.Duration, opts ...ProviderOption) *Provider {
	httpClient = jwksHTTPClient(httpClient)
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
		if opt == nil {
			panic("jwtutil: NewProvider option must not be nil")
		}
		opt(p)
	}
	// Enforce https:// for JWKS unless the caller has explicitly opted into
	// http (e.g. for service-mesh sidecar localhost). A plaintext JWKS lets
	// any on-path attacker inject signing keys, fully forging tokens.
	if err := validateJWKSURL(url, p.allowInsecureURL); err != nil {
		panic("jwtutil: NewProvider JWKS URL is invalid: " + err.Error())
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
	p.stringClaimNames = mergeStringClaimNames(p.extraStringClaims)
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
// The provider stores issuer/audience policy on itself (via [Provider.Verify])
// and does NOT mutate [KeySet.ExpectedIssuer] or [KeySet.ExpectedAudience].
// That makes it safe for two providers to share one parsed *KeySet with
// independent issuer/audience policies — earlier revisions overwrote those
// shared fields on every construction, which leaked the last provider's
// policy into every other provider that aliased the same keyset and could
// race under concurrent construction or verification (R4 fix).
func NewProviderWithKeySet(ks *KeySet, opts ...ProviderOption) *Provider {
	if ks == nil || ks.set == nil || ks.set.Len() == 0 {
		panic("jwtutil: NewProviderWithKeySet requires a non-empty KeySet")
	}
	p := &Provider{keyset: ks, clock: time.Now}
	for _, opt := range opts {
		if opt == nil {
			panic("jwtutil: NewProviderWithKeySet option must not be nil")
		}
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
	p.stringClaimNames = mergeStringClaimNames(p.extraStringClaims)
	return p
}

// Run starts the background JWKS refresh loop. It blocks until ctx is cancelled.
// Call this in a goroutine before serving requests.
func (p *Provider) Run(ctx context.Context) error {
	if p == nil {
		return errors.New("jwtutil: Provider.Run requires a non-nil provider")
	}
	if ctx == nil {
		return errors.New("jwtutil: Provider.Run requires a non-nil context")
	}
	p.runMu.Lock()
	if p.started {
		p.runMu.Unlock()
		return errors.New("jwtutil: Provider.Run already started")
	}
	p.started = true
	p.runMu.Unlock()

	if p.url == "" {
		// No JWKS URL configured (e.g. test provider created via NewProviderWithKeySet).
		// Block until context is cancelled to match the expected lifecycle contract.
		<-ctx.Done()
		return nil
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
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}

	// Phase 2: periodic refresh — failures are non-fatal (cached keys remain valid).
	ticker := time.NewTicker(p.refresh)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.fetch(ctx); err != nil {
				p.logRefreshFailure(err)
			}
		}
	}
}

func (p *Provider) logRefreshFailure(err error) {
	slog.Warn("jwks periodic refresh failed, using cached keys",
		"jwks_configured", p != nil && p.url != "",
		"error_kind", jwksFetchErrorKind(err))
}

func jwksFetchErrorKind(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, ErrJWKSRedirectBlocked) {
		return "redirect_blocked"
	}
	var urlErr *neturl.Error
	if errors.As(err, &urlErr) {
		return "request_failed"
	}
	if errors.Is(err, errJWKSUnexpectedContentType) {
		return "unexpected_content_type"
	}
	if errors.Is(err, errJWKSBadStatus) {
		return "bad_status"
	}
	return "fetch_failed"
}

func defaultHTTPClient() *http.Client {
	// Clone http.DefaultTransport so we keep its proxy handling, dialer
	// timeouts, TLS handshake timeout, idle-conn pool, and HTTP/2 attempt
	// — replacing it wholesale loses every one of those production
	// defaults. We tighten two knobs:
	//
	// MaxResponseHeaderBytes caps the JWKS response header size at 64 KB.
	// The Go default of 0 means "1 MB", plenty for a real JWKS service
	// but enough room for a hostile JWKS endpoint to ship pathological
	// headers (e.g., a SET-COOKIE flood) that bloats memory under
	// attacker influence. The body cap is enforced separately at fetch
	// time (1 MB via io.LimitReader).
	//
	// TLSClientConfig is cloned and raised to TLS 1.2+ so process-wide
	// DefaultTransport customisation cannot silently weaken JWKS fetches.
	//
	// Processes can replace http.DefaultTransport with a custom RoundTripper
	// (otelhttp wrappers, test doubles); falling back to a hand-rolled
	// http.Transport with the standard-library defaults keeps construction
	// panic-free in those processes.
	clone := defaultHTTPTransport()
	return &http.Client{
		Timeout:       defaultHTTPTimeout,
		Transport:     clone,
		CheckRedirect: blockJWKSRedirect,
	}
}

func defaultHTTPTransport() *http.Transport {
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
	clone.TLSClientConfig = cloneTLSConfigWithFloor(clone.TLSClientConfig)
	clone.MaxResponseHeaderBytes = 64 * 1024
	return clone
}

func jwksHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		return defaultHTTPClient()
	}
	// Always clone and re-apply the TLS floor so a fully custom
	// *http.Client cannot bypass the kit's JWKS TLS hardening. Wave
	// 66 closed a hostile-review finding that a user-supplied
	// Transport could ship without the TLS 1.2 floor that
	// defaultHTTPClient enforces.
	//
	// Hardening walks *http.Transport and common wrapper shapes that
	// expose a settable Base/RT/Transport RoundTripper field (otelhttp,
	// retry wrappers). Opaque RoundTrippers (httptest test doubles,
	// fully custom dialers) cannot have a TLS config applied — those
	// are left as-is because they typically do not negotiate TLS, but
	// a production JWKS client should pass *http.Transport (or a
	// wrapper around one) so the floor is load-bearing.
	cloned := *client
	if cloned.Timeout <= 0 {
		cloned.Timeout = defaultHTTPTimeout
	}
	if cloned.Transport == nil {
		cloned.Transport = defaultHTTPTransport()
	} else {
		cloned.Transport = hardenJWKSRoundTripper(cloned.Transport)
	}
	if cloned.CheckRedirect == nil {
		cloned.CheckRedirect = blockJWKSRedirect
	}
	return &cloned
}

// hardenJWKSRoundTripper applies the TLS 1.2 floor to rt when it is an
// *http.Transport, or when it is a wrapper struct with a settable
// Base/RT/Transport field that eventually reaches an *http.Transport.
// Unrecognised RoundTrippers are returned unchanged (see jwksHTTPClient).
func hardenJWKSRoundTripper(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		return defaultHTTPTransport()
	}
	if tr, ok := rt.(*http.Transport); ok {
		hardened := tr.Clone()
		hardened.TLSClientConfig = cloneTLSConfigWithFloor(hardened.TLSClientConfig)
		return hardened
	}
	if hardened, ok := hardenWrappedRoundTripper(rt); ok {
		return hardened
	}
	return rt
}

// hardenWrappedRoundTripper best-effort clones wrapper RoundTrippers that
// expose a settable Base, RT, or Transport field of type http.RoundTripper
// or *http.Transport (the otelhttp.Transport shape). Returns (nil, false)
// when the value is not a settable struct wrapper we recognise.
func hardenWrappedRoundTripper(rt http.RoundTripper) (http.RoundTripper, bool) {
	v := reflect.ValueOf(rt)
	if !v.IsValid() {
		return nil, false
	}
	// Only addressable-copyable pointer-to-struct wrappers.
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil, false
	}
	// Shallow-copy the wrapper so we never mutate the caller's instance.
	orig := v.Elem()
	cpPtr := reflect.New(orig.Type())
	cpPtr.Elem().Set(orig)
	cp := cpPtr.Elem()

	for _, name := range []string{"Base", "RT", "Transport"} {
		f := cp.FieldByName(name)
		if !f.IsValid() || !f.CanSet() {
			continue
		}
		// Field is http.RoundTripper interface or *http.Transport.
		var inner http.RoundTripper
		switch {
		case f.Type() == reflect.TypeOf((*http.Transport)(nil)):
			if f.IsNil() {
				inner = defaultHTTPTransport()
			} else {
				inner = f.Interface().(*http.Transport)
			}
			hardened := hardenJWKSRoundTripper(inner)
			f.Set(reflect.ValueOf(hardened))
			out, _ := cpPtr.Interface().(http.RoundTripper)
			return out, true
		case f.Type().Implements(reflect.TypeOf((*http.RoundTripper)(nil)).Elem()):
			if f.IsNil() {
				inner = defaultHTTPTransport()
			} else {
				inner, _ = f.Interface().(http.RoundTripper)
			}
			hardened := hardenJWKSRoundTripper(inner)
			f.Set(reflect.ValueOf(hardened))
			out, _ := cpPtr.Interface().(http.RoundTripper)
			return out, true
		}
	}
	return nil, false
}

func blockJWKSRedirect(_ *http.Request, _ []*http.Request) error {
	return ErrJWKSRedirectBlocked
}

func cloneTLSConfigWithFloor(cfg *tls.Config) *tls.Config {
	cloned, err := tlsclone.ConfigOrEmptyWithFloor(cfg, minimumTLSVersion)
	if err != nil {
		if errors.Is(err, tlsclone.ErrInsecureSkipVerifyNotPermitted) {
			panic("jwtutil: JWKS HTTP client TLS InsecureSkipVerify=true is not permitted")
		}
		panic("jwtutil: default HTTP client TLS MaxVersion must allow TLS 1.2 or newer")
	}
	return cloned
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
//
// Returns a defensive snapshot — the previous implementation returned the
// live struct, so a caller writing
// `p.KeySet().ExpectedAudience = "x"` mutated verification policy under
// concurrent verifiers. The snapshot shares the underlying jwk.Set
// (immutable through its public API), so the only allocation cost is the
// envelope struct.
//
// Callers that need to distinguish "not ready" from "stale" should use
// [Provider.keySetWithReason] (private) via [Provider.Verify], which
// returns [ErrKeySetNotReady] or [ErrKeySetStale].
func (p *Provider) KeySet() *KeySet {
	ks, _ := p.keySetWithReason()
	if ks == nil {
		return nil
	}
	// Populate issuer/audience from the Provider's set-once policy so a
	// caller that verifies via the snapshot inherits the same guardrails
	// as Provider.Verify (the live keyset never carried these fields).
	return &KeySet{
		set:              ks.set,
		ExpectedIssuer:   p.expectedIssuer,
		ExpectedAudience: p.expectedAudience,
	}
}

// keySetWithReason returns the current keyset along with the typed reason
// when it is unavailable. Returns (ks, nil) when the keyset is usable.
// Errors wrap [ErrKeySetUnavailable] so legacy errors.Is keeps matching.
func (p *Provider) keySetWithReason() (*KeySet, error) {
	if p == nil {
		return nil, ErrKeySetNotReady
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.keyset == nil {
		return nil, ErrKeySetNotReady
	}
	if p.maxStale > 0 && !p.lastSuccessfulFetch.IsZero() {
		clock := p.clock
		if clock == nil {
			clock = time.Now
		}
		if clock().Sub(p.lastSuccessfulFetch) > p.maxStale {
			if !p.staleRejectionCounted.Swap(true) {
				p.fetchFailStaleRejected.Add(1)
			}
			return nil, ErrKeySetStale
		}
	}
	return p.keyset, nil
}

// LastSuccessfulFetch returns the timestamp of the most recent successful
// JWKS fetch, or the zero time if no fetch has succeeded yet. Use for
// staleness alerting / health checks.
func (p *Provider) LastSuccessfulFetch() time.Time {
	if p == nil {
		return time.Time{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastSuccessfulFetch
}

// Staleness returns how long ago the last successful JWKS fetch was, or
// 0 if no fetch has succeeded. A value greater than the configured
// max-stale window means [KeySet] now returns nil.
func (p *Provider) Staleness() time.Duration {
	if p == nil {
		return 0
	}
	last := p.LastSuccessfulFetch()
	if last.IsZero() {
		return 0
	}
	clock := p.clock
	if clock == nil {
		clock = time.Now
	}
	return clock().Sub(last)
}

// Verify validates a token against the Provider's current key set using
// THIS provider's expected issuer and audience policy. Returns
// [ErrKeySetUnavailable] when the key set has not been fetched yet or has
// gone stale past [WithMaxStale]; callers should fail the request closed in
// that case.
//
// Provider.Verify is the safe entry point when a single parsed *KeySet is
// shared by multiple providers with different iss/aud policies — each
// provider passes its own policy into the verifier without touching the
// shared keyset (R4 fix for cross-provider policy bleed).
func (p *Provider) Verify(token string, now time.Time) (*Claims, error) {
	return p.VerifyContext(context.Background(), token, now)
}

// VerifyContext validates a token like [Provider.Verify], using ctx for the
// optional revocation check. Request handlers should prefer this method so a
// Redis/cache-backed revocation lookup observes request cancellation and
// deadlines.
//
// On JWKS unavailability the typed error is one of [ErrKeySetNotReady] or
// [ErrKeySetStale]; both wrap [ErrKeySetUnavailable] so legacy
// errors.Is(err, ErrKeySetUnavailable) callers keep matching unchanged.
func (p *Provider) VerifyContext(ctx context.Context, token string, now time.Time) (*Claims, error) {
	if p == nil {
		return nil, ErrKeySetNotReady
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ks, ksErr := p.keySetWithReason()
	if ksErr != nil {
		return nil, ksErr
	}
	claims, err := verifyToken(ks.set, token, now, p.expectedIssuer, p.expectedAudience, p.stringClaimNames)
	if err != nil {
		return nil, err
	}
	if p.revocation == nil {
		return claims, nil
	}
	if claims.ID == "" {
		return nil, ErrMissingTokenID
	}
	revoked, err := p.revocation.IsRevoked(ctx, claims)
	if err != nil {
		return nil, err
	}
	if revoked {
		return nil, ErrTokenRevoked
	}
	return claims, nil
}

// ErrKeySetUnavailable is the umbrella sentinel returned by [Provider.Verify]
// and [Provider.VerifyContext] when the JWKS has not been fetched yet or has
// gone stale past the max-stale window. Existing callers using
// errors.Is(err, ErrKeySetUnavailable) keep matching for both subcases — the
// typed variants below wrap this sentinel so the contract holds.
//
// New code that needs to distinguish "Provider never fetched" from
// "Provider's last fetch is too old" should check
// [ErrKeySetNotReady] / [ErrKeySetStale] instead.
var ErrKeySetUnavailable = errors.New("jwtutil: key set unavailable")

// ErrKeySetNotReady signals that the Provider has not yet completed its first
// successful JWKS fetch. Returned during early service warmup (Run goroutine
// still retrying) or for a hand-constructed Provider that was never started.
//
// Wraps [ErrKeySetUnavailable] so legacy errors.Is checks keep matching.
var ErrKeySetNotReady = fmt.Errorf("%w: not ready (no successful fetch yet)", ErrKeySetUnavailable)

// ErrKeySetStale signals that the Provider's last successful JWKS fetch is
// older than the configured max-stale window (default 1h; override with
// [WithMaxStale]). A streak of refresh failures is the typical cause.
//
// Wraps [ErrKeySetUnavailable] so legacy errors.Is checks keep matching.
// Operators reading dashboards should treat ErrKeySetStale as a JWKS-side
// outage signal and ErrKeySetNotReady as a warmup signal.
var ErrKeySetStale = fmt.Errorf("%w: stale (last fetch exceeded max-stale window)", ErrKeySetUnavailable)

// ErrJWKSRedirectBlocked is returned by JWKS HTTP clients without an explicit
// redirect policy when a JWKS endpoint attempts to redirect. Fetches must go
// only to the configured signer endpoint unless callers deliberately install a
// custom redirect policy.
var ErrJWKSRedirectBlocked = errors.New("jwtutil: JWKS redirects are disabled by default")

// Internal fetch classification sentinels — matched via errors.Is so
// jwksFetchErrorKind does not depend on Error() string wording.
var (
	errJWKSUnexpectedContentType = errors.New("jwtutil: jwks endpoint returned unexpected content-type")
	errJWKSBadStatus             = errors.New("jwtutil: jwks endpoint returned non-OK status")
)

func (p *Provider) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		p.fetchFailHTTP.Add(1)
		return err
	}
	// Tell the JWKS endpoint we expect JSON. Servers behind captive portals
	// or misconfigured proxies that return text/html still pass our 200
	// check otherwise; the explicit Accept makes the contract loud and
	// gives well-behaved servers a chance to negotiate properly.
	req.Header.Set("Accept", "application/jwk-set+json, application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		p.fetchFailHTTP.Add(1)
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		p.fetchFailHTTP.Add(1)
		return fmt.Errorf("%w: %d", errJWKSBadStatus, resp.StatusCode)
	}

	// Reject non-JSON content types — e.g. captive-portal HTML responses.
	ct, err := singletonContentType(resp.Header)
	if err != nil {
		p.fetchFailHTTP.Add(1)
		return err
	}
	if ct != "" && !isJSONContentType(ct) {
		p.fetchFailHTTP.Add(1)
		return errJWKSUnexpectedContentType
	}

	// 64 KiB is well above any realistic JWKS document (<4 KiB typical)
	// and far below the parse-time DoS threshold of the previous 1 MiB cap.
	const maxJWKSBytes = 64 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		p.fetchFailHTTP.Add(1)
		return err
	}
	if len(body) > maxJWKSBytes {
		p.fetchFailHTTP.Add(1)
		return fmt.Errorf("jwks body exceeds maximum size")
	}

	ks, err := ParseKeySet(body)
	if err != nil {
		p.fetchFailParse.Add(1)
		return err
	}

	p.mu.Lock()
	p.keyset = ks
	p.lastSuccessfulFetch = p.clock()
	p.mu.Unlock()
	p.staleRejectionCounted.Store(false)
	return nil
}

func singletonContentType(h http.Header) (string, error) {
	values := h.Values("Content-Type")
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("jwks endpoint returned multiple content-type headers")
	}
	return strings.TrimSpace(values[0]), nil
}

// requireExpectedJWTType inspects the protected JOSE header's `typ`
// field on a parsed token. Empty `typ` is accepted (many issuers omit
// it for vanilla JWTs). When present, the value must be one of "JWT"
// or "at+jwt" (RFC 9068 access tokens); anything else — including
// "JWE", "OAuth2 cookie", or a future custom type — is rejected as
// ErrSignatureInvalid-shaped error so a cross-token-type confusion
// attack cannot reuse a same-key-signed non-access-token.
func requireExpectedJWTType(tokenString string) error {
	// Decode only the protected header segment (first compact-JWS part)
	// so we do not re-parse payload+signature after jwt.Parse already
	// verified the token.
	firstDot := strings.IndexByte(tokenString, '.')
	if firstDot <= 0 {
		return errors.New("jwtutil: missing JWS header")
	}
	headerB64 := tokenString[:firstDot]
	raw, err := base64.RawURLEncoding.DecodeString(headerB64)
	if err != nil {
		// Some issuers pad; tolerate standard encoding as a fallback.
		raw, err = base64.URLEncoding.DecodeString(headerB64)
		if err != nil {
			return fmt.Errorf("jwtutil: decode JOSE header: %w", err)
		}
	}
	var hdr struct {
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(raw, &hdr); err != nil {
		return fmt.Errorf("jwtutil: parse JOSE header: %w", err)
	}
	typ := strings.TrimSpace(hdr.Typ)
	if typ == "" {
		return nil
	}
	// Compare case-insensitively per RFC 7519 §5.1.
	switch strings.ToLower(typ) {
	case "jwt", "at+jwt":
		return nil
	default:
		return fmt.Errorf("jwtutil: unexpected JOSE header typ %q (want JWT or at+jwt)", typ)
	}
}
