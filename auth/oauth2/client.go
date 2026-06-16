package oauth2

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/coreos/go-oidc/v3/oidc"
	xoauth2 "golang.org/x/oauth2"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/secret"
)

// Client is the kit's OAuth2/OIDC relying-party client. Composes
// [golang.org/x/oauth2.Config] for the OAuth2 dance and
// [github.com/coreos/go-oidc/v3/oidc.Provider] for issuer discovery +
// ID-token verification. Concurrency-safe after construction.
type Client struct {
	cfg          Config
	provider     *oidc.Provider
	oauth        *xoauth2.Config
	verifier     *oidc.IDTokenVerifier
	httpClient   *http.Client
	sessions     SessionStore
	state        StateStore
	logger       *slog.Logger
	usePKCE      bool
	sessionTTL   time.Duration
	stateTTL     time.Duration
	cookieName   string
	cookieDomain string
	cookieSecure bool
}

// Option configures a [Client].
type Option func(*Client)

// WithHTTPClient overrides the http.Client used for discovery + token
// + userinfo. Default: 10s timeout. Threaded through go-oidc via
// [oidc.ClientContext].
func WithHTTPClient(c *http.Client) Option {
	if c == nil {
		panic("oauth2: WithHTTPClient requires non-nil client")
	}
	return func(cl *Client) { cl.httpClient = c }
}

// WithSessionStore wires the persistence backend for logged-in
// sessions. Required.
func WithSessionStore(s SessionStore) Option {
	if s == nil {
		panic("oauth2: WithSessionStore requires non-nil store")
	}
	return func(cl *Client) { cl.sessions = s }
}

// WithStateStore wires the persistence backend for in-flight login
// state (PKCE verifier + OIDC nonce). Required.
func WithStateStore(s StateStore) Option {
	if s == nil {
		panic("oauth2: WithStateStore requires non-nil store")
	}
	return func(cl *Client) { cl.state = s }
}

// WithLogger overrides the slog logger.
func WithLogger(l *slog.Logger) Option {
	return func(cl *Client) { cl.logger = l }
}

// WithoutPKCE disables PKCE. Allowed only for confidential clients
// on providers that don't support PKCE — most modern providers do.
// Discouraged.
func WithoutPKCE() Option {
	return func(cl *Client) { cl.usePKCE = false }
}

// WithSessionTTL overrides the session TTL (default 24h).
func WithSessionTTL(d time.Duration) Option {
	if d <= 0 {
		panic("oauth2: WithSessionTTL requires positive duration")
	}
	return func(cl *Client) { cl.sessionTTL = d }
}

// WithStateTTL overrides the in-flight state TTL (default 10m).
func WithStateTTL(d time.Duration) Option {
	if d <= 0 {
		panic("oauth2: WithStateTTL requires positive duration")
	}
	return func(cl *Client) { cl.stateTTL = d }
}

// WithCookieName overrides "kit_oauth_session".
func WithCookieName(name string) Option {
	if name == "" {
		panic("oauth2: WithCookieName requires non-empty name")
	}
	return func(cl *Client) { cl.cookieName = name }
}

// WithCookieDomain restricts the cookie to a domain.
func WithCookieDomain(d string) Option {
	return func(cl *Client) { cl.cookieDomain = d }
}

// WithInsecureCookie disables Secure for local-dev over plain HTTP.
func WithInsecureCookie() Option {
	return func(cl *Client) { cl.cookieSecure = false }
}

// NewClient constructs a Client. Performs OIDC discovery via go-oidc
// (which validates the discovered issuer matches the configured one
// per RFC 8414 §3.3) and constructs the underlying oauth2.Config +
// IDTokenVerifier. Fails fast on bad config OR unreachable issuer.
func NewClient(ctx context.Context, cfg Config, opts ...Option) (*Client, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("oauth2: Config.Issuer is required")
	}
	if cfg.ClientID == "" {
		return nil, errors.New("oauth2: Config.ClientID is required")
	}
	if cfg.RedirectURL == "" {
		return nil, errors.New("oauth2: Config.RedirectURL is required")
	}
	c := &Client{
		cfg:          cfg,
		usePKCE:      true,
		sessionTTL:   24 * time.Hour,
		stateTTL:     10 * time.Minute,
		cookieName:   "kit_oauth_session",
		cookieSecure: true,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("oauth2: option must not be nil")
		}
		opt(c)
	}
	if c.sessions == nil {
		return nil, errors.New("oauth2: WithSessionStore is required")
	}
	if c.state == nil {
		return nil, errors.New("oauth2: WithStateStore is required")
	}
	// Fail closed: a public client (no client secret) with PKCE disabled
	// has neither authorization-code-interception protection — exactly
	// the configuration RFC 7636 forbids. WithoutPKCE is only valid for
	// confidential clients that present a secret at the token endpoint.
	if !c.usePKCE && (cfg.ClientSecret == nil || cfg.ClientSecret.IsEmpty()) {
		return nil, errors.New("oauth2: WithoutPKCE requires a confidential client (Config.ClientSecret); a public client without PKCE has no code-interception protection")
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	// go-oidc honours the http.Client stashed on ctx via
	// oidc.ClientContext (which it forwards into its internal
	// discovery + JWKS fetcher).
	discoveryCtx := oidc.ClientContext(ctx, c.httpClient)
	provider, err := oidc.NewProvider(discoveryCtx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIssuerDiscovery, err)
	}
	c.provider = provider

	scopes := cfg.Scopes
	if !containsScope(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}

	var secretStr string
	if cfg.ClientSecret != nil && !cfg.ClientSecret.IsEmpty() {
		secretStr = cfg.ClientSecret.RevealString()
	}
	c.oauth = &xoauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: secretStr,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       scopes,
	}
	c.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	return c, nil
}

func containsScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

// safeRelativeRedirect reports whether rawTarget is a safe origin-relative
// redirect target (e.g. "/dashboard", "/dashboard?tab=billing", "#done")
// and returns its canonical form. It rejects absolute URLs, scheme-relative
// "//host" and "\\host" prefixes, userinfo, backslashes, control bytes, and
// surrounding whitespace — any of which would let an attacker-controlled
// redirect_to escape the current origin (open redirect). The kit's
// httpx.SafeRedirect lives in a sibling module; this is the dependency-free
// equivalent for the relative-only case this package needs.
func safeRelativeRedirect(rawTarget string) (string, bool) {
	if rawTarget == "" {
		return "", false
	}
	if strings.TrimSpace(rawTarget) != rawTarget {
		return "", false
	}
	for _, r := range rawTarget {
		if r == '\\' || unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", false
		}
	}
	u, err := url.Parse(rawTarget)
	if err != nil {
		return "", false
	}
	// Must be purely relative: no scheme, no host, no userinfo.
	if u.Scheme != "" || u.Host != "" || u.User != nil {
		return "", false
	}
	// Reject scheme-relative paths ("//evil", "/\evil", encoded forms)
	// which browsers resolve as absolute to another origin.
	if hasSchemeRelativePath(u) {
		return "", false
	}
	return u.String(), true
}

func hasSchemeRelativePath(u *url.URL) bool {
	path := u.EscapedPath()
	if path == "" {
		path = u.Path
	}
	if isDoubleSlashPrefix(path) {
		return true
	}
	unescaped, err := url.PathUnescape(path)
	return err != nil || isDoubleSlashPrefix(unescaped)
}

func isDoubleSlashPrefix(path string) bool {
	isSlash := func(b byte) bool { return b == '/' || b == '\\' }
	return len(path) >= 2 && isSlash(path[0]) && isSlash(path[1])
}

// Handlers returns the http.Handler that serves /login, /callback,
// /logout under a path prefix. Mount with mux.Handle("/oauth/", ...).
func (c *Client) Handlers() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", c.handleLogin)
	mux.HandleFunc("GET /callback", c.handleCallback)
	mux.HandleFunc("POST /logout", c.handleLogout)
	return http.StripPrefix("/oauth", mux)
}

// handleLogin generates state + nonce (+ PKCE verifier when enabled),
// persists them, and redirects to the issuer's authorization endpoint
// via golang.org/x/oauth2.AuthCodeURL — which handles encoding,
// scope joining, and PKCE challenge construction.
func (c *Client) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := generateRandomToken()
	if err != nil {
		http.Error(w, "oauth2: state generation failed", http.StatusInternalServerError)
		return
	}
	nonce, err := generateRandomToken()
	if err != nil {
		http.Error(w, "oauth2: nonce generation failed", http.StatusInternalServerError)
		return
	}
	entry := StateEntry{Nonce: nonce, CreatedAt: time.Now()}
	// Only persist a post-login deep link when it is an origin-relative
	// path. Absolute or scheme-relative targets are dropped to prevent
	// an attacker-controlled redirect_to turning the callback into an
	// open redirect (phishing / token-relay vector).
	if redirectTo := r.URL.Query().Get("redirect_to"); redirectTo != "" {
		if safe, ok := safeRelativeRedirect(redirectTo); ok {
			entry.RedirectTo = safe
		} else {
			c.logger.WarnContext(r.Context(), "oauth2: dropped unsafe redirect_to",
				slog.String("redirect_to", redact.StringValue(redirectTo)))
		}
	}

	authOpts := []xoauth2.AuthCodeOption{
		oidc.Nonce(nonce),
	}
	if c.usePKCE {
		// golang.org/x/oauth2 mints the verifier + paired S256
		// challenge for us. The verifier rides in the StateStore
		// so the callback can complete the exchange.
		verifier := xoauth2.GenerateVerifier()
		entry.CodeVerifier = verifier
		authOpts = append(authOpts, xoauth2.S256ChallengeOption(verifier))
	}

	if err := c.state.Put(r.Context(), state, entry, c.stateTTL); err != nil {
		c.logger.WarnContext(r.Context(), "oauth2: state.Put failed", slog.String("error", err.Error()))
		http.Error(w, "oauth2: state persistence failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, c.oauth.AuthCodeURL(state, authOpts...), http.StatusFound)
}

// handleCallback exchanges the authorization code for tokens, verifies
// the ID token via go-oidc (signature + alg + exp + audience + iss),
// double-checks the nonce, persists the session, and sets the cookie.
func (c *Client) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		// The issuer may redirect back with attacker-influenced `error` /
		// `error_description` values. Log them (redacted) server-side and
		// return only the opaque sentinel across the trust boundary so the
		// caller can't surface arbitrary reflected content.
		c.logger.WarnContext(r.Context(), "oauth2: provider returned an error",
			slog.String("error", redact.StringValue(errParam)),
			slog.String("error_description", redact.StringValue(q.Get("error_description"))))
		http.Error(w, ErrProviderError.Error(), http.StatusBadRequest)
		return
	}
	stateToken := q.Get("state")
	code := q.Get("code")
	if stateToken == "" || code == "" {
		http.Error(w, "oauth2: missing state or code", http.StatusBadRequest)
		return
	}
	entry, err := c.state.Get(r.Context(), stateToken)
	if err != nil {
		c.logger.WarnContext(r.Context(), "oauth2: callback state lookup failed", slog.String("error", err.Error()))
		http.Error(w, ErrStateMismatch.Error(), http.StatusBadRequest)
		return
	}
	// Single-use state: delete before exchange so a replay can't
	// succeed even on a slow exchange.
	_ = c.state.Delete(r.Context(), stateToken)

	exchangeCtx := oidc.ClientContext(r.Context(), c.httpClient)
	exchangeOpts := []xoauth2.AuthCodeOption{}
	if c.usePKCE && entry.CodeVerifier != "" {
		exchangeOpts = append(exchangeOpts, xoauth2.VerifierOption(entry.CodeVerifier))
	}
	token, err := c.oauth.Exchange(exchangeCtx, code, exchangeOpts...)
	if err != nil {
		c.logger.WarnContext(r.Context(), "oauth2: code exchange failed", redact.Error(err))
		http.Error(w, ErrCodeExchange.Error(), http.StatusBadGateway)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		http.Error(w, "oauth2: token response missing id_token", http.StatusBadGateway)
		return
	}
	idToken, err := c.verifier.Verify(exchangeCtx, rawIDToken)
	if err != nil {
		// The verifier error can embed token claims, signature details,
		// or the issuer's raw endpoint body. Log it redacted and return
		// only the opaque sentinel across the trust boundary.
		c.logger.WarnContext(r.Context(), "oauth2: id_token verify failed", redact.Error(err))
		http.Error(w, ErrCodeExchange.Error(), http.StatusBadRequest)
		return
	}
	if idToken.Nonce != entry.Nonce {
		http.Error(w, ErrNonceMismatch.Error(), http.StatusBadRequest)
		return
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, fmt.Sprintf("oauth2: id_token claims: %v", err), http.StatusBadGateway)
		return
	}

	sessionID, err := generateRandomToken()
	if err != nil {
		http.Error(w, "oauth2: session id generation failed", http.StatusInternalServerError)
		return
	}
	sess := Session{
		SessionID:    sessionID,
		UserID:       idToken.Subject,
		AccessToken:  secret.NewFromString(token.AccessToken),
		RefreshToken: secret.NewFromString(token.RefreshToken),
		Expiry:       token.Expiry,
		Claims:       claims,
	}
	if err := c.sessions.Put(r.Context(), sessionID, sess, c.sessionTTL); err != nil {
		http.Error(w, "oauth2: session persistence failed", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, c.sessionCookie(sessionID))
	// Defence in depth: re-validate the persisted deep link before
	// emitting it in a Location header. handleLogin already screens
	// redirect_to, but a hostile StateStore implementation could return
	// an unsafe value, so never redirect to anything that isn't an
	// origin-relative path.
	if entry.RedirectTo != "" {
		if safe, ok := safeRelativeRedirect(entry.RedirectTo); ok {
			http.Redirect(w, r, safe, http.StatusFound)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLogout drops the session and clears the cookie.
func (c *Client) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(c.cookieName)
	if err == nil {
		_ = c.sessions.Delete(r.Context(), cookie.Value)
	}
	// Mirror Domain (and other scoping attributes) from sessionCookie so a
	// domain-scoped session cookie set via WithCookieDomain is actually
	// matched and removed by the browser; otherwise the stale cookie value
	// would linger client-side.
	http.SetCookie(w, &http.Cookie{
		Name:     c.cookieName,
		Value:    "",
		Path:     "/",
		Domain:   c.cookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (c *Client) sessionCookie(sessionID string) *http.Cookie {
	return &http.Cookie{
		Name:     c.cookieName,
		Value:    sessionID,
		Path:     "/",
		Domain:   c.cookieDomain,
		HttpOnly: true,
		Secure:   c.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(c.sessionTTL),
	}
}

// OAuth2Config returns the underlying *xoauth2.Config so callers can
// build refresh-token transports (oauth2.Token{RefreshToken: ...}.
// Client(ctx)) or per-request OAuth2 transports without re-discovering
// endpoints. Returned value MUST NOT be mutated.
func (c *Client) OAuth2Config() *xoauth2.Config { return c.oauth }

// Provider returns the underlying *oidc.Provider for callers needing
// UserInfo or non-standard discovery fields.
func (c *Client) Provider() *oidc.Provider { return c.provider }
