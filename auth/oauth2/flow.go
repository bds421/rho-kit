package oauth2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	xoauth2 "golang.org/x/oauth2"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Flow runs the browser sign-in of a relying party that keeps its own
// sessions: [Flow.Start] sends the browser to the provider, [Flow.Finish]
// verifies its answer and returns the verified identity. Flow sets no
// session cookie and stores no tokens; the caller mints its own session
// from the [Result], with whatever cookie attributes it needs.
//
// The provider's [Config] is passed per sign-in, so providers can be added,
// changed and removed while the process runs: Start takes it, and Finish
// asks a resolver for it by the key Start stored with the state. Discovery
// documents are cached per issuer for the discovery TTL (default one hour).
//
// The same protections as [Client] apply: state and nonce, PKCE (S256), a
// browser binding cookie per pending sign-in (SameSite=Lax, since the
// provider's redirect back is a cross-site navigation), single-use state
// and ID-token verification by go-oidc. Flow is safe for concurrent use.
type Flow struct {
	state        StateStore
	httpClient   *http.Client
	stateTTL     time.Duration
	discoveryTTL time.Duration
	cookiePrefix string
	cookiePath   string
	cookieSecure bool
	now          func() time.Time

	mu        sync.Mutex
	providers map[string]cachedProvider
}

type cachedProvider struct {
	provider *oidc.Provider
	fetched  time.Time
}

// FlowOption configures a [Flow].
type FlowOption func(*Flow)

// WithFlowHTTPClient sets the client for discovery, JWKS and token calls,
// for example one whose dialer refuses private addresses. Default: a
// client with a 10-second timeout.
func WithFlowHTTPClient(c *http.Client) FlowOption { return func(f *Flow) { f.httpClient = c } }

// WithFlowStateTTL bounds how long a started sign-in may take (default 10
// minutes).
func WithFlowStateTTL(d time.Duration) FlowOption { return func(f *Flow) { f.stateTTL = d } }

// WithFlowDiscoveryTTL sets how long a provider's discovery document is
// reused (default one hour). JWKS rotation is go-oidc's and independent.
func WithFlowDiscoveryTTL(d time.Duration) FlowOption { return func(f *Flow) { f.discoveryTTL = d } }

// WithFlowCookie sets the binding cookies' name prefix and path. Default:
// "__Host-oidc_state_" on "/". A __Host- prefix needs the path "/".
func WithFlowCookie(prefix, path string) FlowOption {
	return func(f *Flow) { f.cookiePrefix, f.cookiePath = prefix, path }
}

// WithFlowInsecureCookie drops the Secure attribute, for plain-http tests
// only. It requires a cookie prefix without __Host-.
func WithFlowInsecureCookie() FlowOption { return func(f *Flow) { f.cookieSecure = false } }

// WithFlowClock replaces time.Now, for tests.
func WithFlowClock(now func() time.Time) FlowOption { return func(f *Flow) { f.now = now } }

// NewFlow returns a Flow keeping its pending sign-ins in state. Use a
// shared store (auth/oauth2/redis) when several replicas serve callbacks.
func NewFlow(state StateStore, opts ...FlowOption) (*Flow, error) {
	if state == nil {
		return nil, errors.New("oauth2: NewFlow requires a StateStore")
	}
	f := &Flow{
		state:        state,
		stateTTL:     10 * time.Minute,
		discoveryTTL: time.Hour,
		cookiePrefix: "__Host-oidc_state_",
		cookiePath:   "/",
		cookieSecure: true,
		now:          time.Now,
		providers:    make(map[string]cachedProvider),
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("oauth2: flow option must not be nil")
		}
		opt(f)
	}
	if f.httpClient == nil {
		// kit-doctor:allow default-http-client reason="callers inject a hardened client with WithFlowHTTPClient"
		f.httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	if f.stateTTL <= 0 || f.discoveryTTL <= 0 || f.cookiePrefix == "" {
		return nil, errors.New("oauth2: flow TTLs must be positive and the cookie prefix set")
	}
	if len(f.cookiePrefix) >= 7 && f.cookiePrefix[:7] == "__Host-" && (f.cookiePath != "/" || !f.cookieSecure) {
		return nil, errors.New("oauth2: a __Host- cookie prefix requires Secure cookies on path /")
	}
	return f, nil
}

func (f *Flow) binding() binding {
	return binding{
		prefix:   f.cookiePrefix,
		path:     f.cookiePath,
		secure:   f.cookieSecure,
		lifetime: f.stateTTL,
	}
}

// StartRequest is one sign-in to begin.
type StartRequest struct {
	// Config is the provider to sign in with.
	Config Config
	// Key names Config for [Flow.Finish]'s resolver, such as a provider id.
	Key string
	// Data is kept with the state and returned by Finish, for example
	// whether the sign-in links an account. Keep it small.
	Data string
	// RedirectTo is an origin-relative path returned by Finish; any other
	// value is dropped.
	RedirectTo string
	// Prompt (space-separated values), MaxAge and LoginHint are passed to the provider when set,
	// validated as [Client]'s login parameters are.
	Prompt    string
	MaxAge    *int
	LoginHint string
}

// Result is a verified sign-in.
type Result struct {
	// Key and Data are what [Flow.Start] was given.
	Key  string
	Data string
	// RedirectTo is the validated deep link of the start, or empty.
	RedirectTo string
	// Issuer and Subject identify the person at the provider; the pair is
	// the stable identity (OpenID Connect Core 1.0 section 5.7).
	Issuer  string
	Subject string
	// Claims are the verified ID token's claims.
	Claims   map[string]any
	AMR      []string
	AuthTime time.Time
}

// Start begins a sign-in: it stores state, nonce and PKCE verifier, binds
// the state to this browser with a cookie on w and returns the provider's
// authorization URL, which the caller redirects to (or hands to a script
// that navigates). It fails with [ErrIssuerDiscovery] when the provider
// cannot be discovered.
func (f *Flow) Start(w http.ResponseWriter, r *http.Request, req StartRequest) (string, error) {
	params, err := req.loginParams()
	if err != nil {
		return "", err
	}
	if err := checkConfig(req.Config); err != nil {
		return "", err
	}
	provider, err := f.provider(r.Context(), req.Config.Issuer)
	if err != nil {
		return "", err
	}
	state, err := generateRandomToken()
	if err != nil {
		return "", err
	}
	nonce, err := generateRandomToken()
	if err != nil {
		return "", err
	}
	verifier := xoauth2.GenerateVerifier()
	entry := StateEntry{
		Nonce: nonce, CodeVerifier: verifier, CreatedAt: f.now(), MaxAge: params.maxAge,
		Key: req.Key, Data: req.Data, Issuer: req.Config.Issuer,
	}
	if safe, ok := safeRelativeRedirect(req.RedirectTo); ok {
		entry.RedirectTo = safe
	}
	if err := f.state.Put(r.Context(), state, entry, f.stateTTL); err != nil {
		return "", redact.WrapError("oauth2: store state", err)
	}
	f.binding().start(w, r, state, entry.CreatedAt)
	opts := append([]xoauth2.AuthCodeOption{oidc.Nonce(nonce), xoauth2.S256ChallengeOption(verifier)}, params.authCodeOptions()...)
	return oauthConfig(req.Config, provider.Endpoint()).AuthCodeURL(state, opts...), nil
}

func (req StartRequest) loginParams() (loginParams, error) {
	q := url.Values{}
	if req.Prompt != "" {
		q.Set("prompt", req.Prompt)
	}
	if req.MaxAge != nil {
		q.Set("max_age", strconv.Itoa(*req.MaxAge))
	}
	if req.LoginHint != "" {
		q.Set("login_hint", req.LoginHint)
	}
	return parseLoginParams(q)
}

func checkConfig(cfg Config) error {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return errors.New("oauth2: Config needs Issuer, ClientID and RedirectURL")
	}
	return nil
}

// Resolver returns the provider configuration a sign-in started with, by
// its [StartRequest.Key]. An error ends the sign-in and is returned by
// [Flow.Finish] as it is, so a provider removed meanwhile is refused.
type Resolver func(ctx context.Context, key string) (Config, error)

// Finish completes the sign-in the provider redirected back to (the
// request to the callback URL). It clears the browser binding on w and
// returns the verified identity; it never writes a body, so the caller
// answers. Its errors are the sentinels [ErrStateMismatch] (no binding in
// this browser: restart the sign-in), [ErrStateNotFound], [ErrProviderError],
// [ErrCodeExchange], [ErrIDTokenInvalid], [ErrNonceMismatch],
// [ErrStaleAuthentication], [ErrIssuerMismatch] (an answer of another
// provider, the mix-up attack of RFC 9207) and [ErrIssuerDiscovery], or
// the resolver's.
// None of them carries what the provider or the token said.
func (f *Flow) Finish(w http.ResponseWriter, r *http.Request, resolve Resolver) (Result, error) {
	q := r.URL.Query()
	state := q.Get("state")
	b := f.binding()
	if !b.bound(r, state) {
		return Result{}, ErrStateMismatch
	}
	b.clear(w, state)
	entry, err := f.take(r.Context(), state)
	if err != nil {
		return Result{}, err
	}
	if q.Get("error") != "" {
		return Result{}, ErrProviderError
	}
	if f.now().Sub(entry.CreatedAt) > f.stateTTL {
		return Result{}, ErrStateNotFound
	}
	code := q.Get("code")
	if code == "" {
		return Result{}, ErrProviderError
	}
	cfg, err := resolve(r.Context(), entry.Key)
	if err != nil {
		return Result{}, err
	}
	provider, err := f.answeredBy(r, cfg, entry)
	if err != nil {
		return Result{}, err
	}
	return f.exchange(r.Context(), provider, cfg, entry, code)
}

// answeredBy checks that the callback answers the sign-in's own provider
// before any code is sent anywhere (RFC 9207, mix-up attacks): the
// configuration of its key still names the issuer it started with, the
// request came to that configuration's callback URL, and the iss parameter
// names that issuer exactly when the provider says it sends one, or when
// one is sent at all.
func (f *Flow) answeredBy(r *http.Request, cfg Config, entry StateEntry) (*oidc.Provider, error) {
	if err := checkConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.Issuer != entry.Issuer || !sameCallback(r, cfg.RedirectURL) {
		return nil, ErrIssuerMismatch
	}
	provider, err := f.provider(r.Context(), cfg.Issuer)
	if err != nil {
		return nil, err
	}
	var meta struct {
		IssParameter bool `json:"authorization_response_iss_parameter_supported"`
	}
	if err := provider.Claims(&meta); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIssuerDiscovery, redact.WrapError("discovery claims", err))
	}
	q := r.URL.Query()
	if (meta.IssParameter || q.Has("iss")) && q.Get("iss") != cfg.Issuer {
		return nil, ErrIssuerMismatch
	}
	return provider, nil
}

// sameCallback reports whether r came to the path of redirectURL.
func sameCallback(r *http.Request, redirectURL string) bool {
	u, err := url.Parse(redirectURL)
	return err == nil && u.EscapedPath() == r.URL.EscapedPath()
}

func (f *Flow) take(ctx context.Context, state string) (StateEntry, error) {
	if taker, ok := f.state.(stateTaker); ok {
		return taker.Take(ctx, state)
	}
	entry, err := f.state.Get(ctx, state)
	if err != nil {
		return StateEntry{}, err
	}
	if err := f.state.Delete(ctx, state); err != nil {
		// A state that may be replayed is not used.
		return StateEntry{}, redact.WrapError("oauth2: delete state", err)
	}
	return entry, nil
}

func (f *Flow) exchange(ctx context.Context, provider *oidc.Provider, cfg Config, entry StateEntry, code string) (Result, error) {
	exchangeCtx := oidc.ClientContext(ctx, f.httpClient)
	token, err := oauthConfig(cfg, provider.Endpoint()).Exchange(exchangeCtx, code, xoauth2.VerifierOption(entry.CodeVerifier))
	if err != nil {
		return Result{}, ErrCodeExchange
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Result{}, ErrCodeExchange
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID, Now: f.now}).Verify(exchangeCtx, rawIDToken)
	if err != nil {
		return Result{}, ErrIDTokenInvalid
	}
	if idToken.Nonce != entry.Nonce {
		return Result{}, ErrNonceMismatch
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return Result{}, ErrIDTokenInvalid
	}
	amr, authTime := authenticationClaims(claims)
	if entry.MaxAge != nil && !satisfiesMaxAge(authTime, entry.CreatedAt, *entry.MaxAge) {
		return Result{}, ErrStaleAuthentication
	}
	return Result{
		Key: entry.Key, Data: entry.Data, RedirectTo: entry.RedirectTo,
		Issuer: idToken.Issuer, Subject: idToken.Subject,
		Claims: claims, AMR: amr, AuthTime: authTime,
	}, nil
}

// provider returns the discovered provider of issuer, from the cache while
// it is younger than the discovery TTL. go-oidc checks that the document
// names the same issuer.
func (f *Flow) provider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	f.mu.Lock()
	cached, ok := f.providers[issuer]
	f.mu.Unlock()
	if ok && f.now().Sub(cached.fetched) < f.discoveryTTL {
		return cached.provider, nil
	}
	// The provider keeps its context for JWKS refreshes; see Client.discover.
	// The http client's timeout bounds the discovery itself.
	provider, err := oidc.NewProvider(oidc.ClientContext(context.WithoutCancel(ctx), f.httpClient), issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIssuerDiscovery, redact.WrapError("discovery", err))
	}
	f.mu.Lock()
	f.providers[issuer] = cachedProvider{provider: provider, fetched: f.now()}
	f.mu.Unlock()
	return provider, nil
}

// Forget drops the cached discovery of issuer, for example after its
// configuration changed.
func (f *Flow) Forget(issuer string) {
	f.mu.Lock()
	delete(f.providers, issuer)
	f.mu.Unlock()
}
