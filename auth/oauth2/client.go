package oauth2

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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
		// kit-doctor:allow default-http-client reason="OAuth2 keeps its module dependency-light and applies an explicit timeout; callers can inject a hardened client with WithHTTPClient"
		c.httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	// go-oidc's RemoteKeySet retains the construction context for every
	// later JWKS refresh. A caller wrapping NewClient in
	// context.WithTimeout(...); defer cancel() would otherwise leave the
	// provider using a cancelled ctx after construction, so every key
	// rotation fails with "context canceled" until process restart.
	// Honour the caller's ctx for the initial discovery round-trip via
	// the http.Client timeout, but bind the long-lived provider to a
	// non-cancellable context so JWKS refresh outlives construction.
	discoveryCtx := oidc.ClientContext(context.WithoutCancel(ctx), c.httpClient)
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
// /logout under the fixed mount prefix "/oauth". Mount exactly as:
//
//	mux.Handle("/oauth/", client.Handlers())
//
// which exposes /oauth/login, /oauth/callback, /oauth/logout. Mounting
// under any other prefix silently 404s because StripPrefix only strips
// "/oauth". Callers that need a different prefix should wire the three
// handler methods themselves (or wrap the returned handler with their
// own StripPrefix after forking this shape).
func (c *Client) Handlers() Handlers {
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
	// Bind the CSRF state to the initiating browser. Server-side
	// single-use storage alone only prevents replay; without a
	// browser-bound cookie an attacker can complete login as
	// themselves and lure the victim into consuming the callback
	// (login CSRF / session swap). The cookie carries a hash of the
	// state so the raw state is not double-exposed.
	http.SetCookie(w, c.stateCookie(stateBindingValue(state)))
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
	// Browser binding: the state cookie set at /login must match this
	// callback's state. Presence in StateStore alone is not enough —
	// that would accept a state minted for a different browser
	// (login CSRF / session swap).
	stateCookie, err := r.Cookie(c.stateCookieName())
	if err != nil || stateCookie.Value == "" {
		c.logger.WarnContext(r.Context(), "oauth2: callback missing state cookie")
		http.Error(w, ErrStateMismatch.Error(), http.StatusBadRequest)
		return
	}
	expected := stateBindingValue(stateToken)
	if subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(expected)) != 1 {
		c.logger.WarnContext(r.Context(), "oauth2: callback state cookie mismatch")
		http.Error(w, ErrStateMismatch.Error(), http.StatusBadRequest)
		return
	}
	// Clear the one-shot binding cookie whether the rest of the flow
	// succeeds or fails.
	http.SetCookie(w, c.clearStateCookie())

	entry, err := c.takeState(r.Context(), stateToken)
	if err != nil {
		if errors.Is(err, ErrStateNotFound) {
			c.logger.WarnContext(r.Context(), "oauth2: callback state mismatch", redact.Error(err))
			http.Error(w, ErrStateMismatch.Error(), http.StatusBadRequest)
			return
		}
		// Infrastructure failures (Redis down, timeouts) must not be
		// reported as "state mismatch" — that misleads operators and
		// confuses attack-signal dashboards with availability faults.
		c.logger.WarnContext(r.Context(), "oauth2: callback state store failed", redact.Error(err))
		http.Error(w, "oauth2: state store unavailable", http.StatusServiceUnavailable)
		return
	}

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
		http.Error(w, ErrIDTokenInvalid.Error(), http.StatusBadRequest)
		return
	}
	if idToken.Nonce != entry.Nonce {
		http.Error(w, ErrNonceMismatch.Error(), http.StatusBadRequest)
		return
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		c.logger.WarnContext(r.Context(), "oauth2: id_token claims decode failed", redact.Error(err))
		http.Error(w, ErrIDTokenInvalid.Error(), http.StatusBadGateway)
		return
	}

	sessionID, err := generateRandomToken()
	if err != nil {
		http.Error(w, "oauth2: session id generation failed", http.StatusInternalServerError)
		return
	}
	sess := Session{
		SessionID:   sessionID,
		UserID:      idToken.Subject,
		AccessToken: secret.NewFromString(token.AccessToken),
		Expiry:      token.Expiry,
		Claims:      claims,
	}
	// Nil when the issuer did not grant a refresh token (documented
	// Session.RefreshToken contract).
	if token.RefreshToken != "" {
		sess.RefreshToken = secret.NewFromString(token.RefreshToken)
	}
	if err := c.sessions.Put(r.Context(), sessionID, sess, c.sessionTTL); err != nil {
		c.logger.WarnContext(r.Context(), "oauth2: session.Put failed", redact.Error(err))
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
	// No deep link: leave the browser on a human-readable landing page
	// rather than a blank 204 body (common when SPA apps forgot to pass
	// redirect_to). Operators can still override via redirect_to.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<!doctype html><title>Signed in</title><p>Signed in. You can close this window.</p>"))
}

// handleLogout drops the session and clears the cookie.
func (c *Client) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(c.cookieName)
	if err == nil && cookie.Value != "" {
		if delErr := c.sessions.Delete(r.Context(), cookie.Value); delErr != nil {
			// Logout is a security-relevant state change. Cookie is still
			// cleared client-side so the browser stops sending the id, but
			// a store fault means the server-side session may remain
			// usable if the cookie is later re-presented (stolen cookie).
			c.logger.ErrorContext(r.Context(), "oauth2: session.Delete failed on logout",
				redact.Error(delErr))
			http.Error(w, "oauth2: session revocation failed", http.StatusInternalServerError)
			// Still clear the cookie so the client stops presenting it.
			http.SetCookie(w, c.clearSessionCookie())
			return
		}
	}
	// Mirror Domain (and other scoping attributes) from sessionCookie so a
	// domain-scoped session cookie set via WithCookieDomain is actually
	// matched and removed by the browser; otherwise the stale cookie value
	// would linger client-side.
	http.SetCookie(w, c.clearSessionCookie())
	w.WriteHeader(http.StatusNoContent)
}

func (c *Client) clearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     c.cookieName,
		Value:    "",
		Path:     "/",
		Domain:   c.cookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

// stateTaker is optionally implemented by StateStore backends that can
// atomically consume a one-shot state entry (GETDEL / Take).
type stateTaker interface {
	Take(ctx context.Context, state string) (StateEntry, error)
}

// takeState consumes a single-use login state. Prefer atomic Take when
// the store implements it; otherwise Get-then-Delete with Delete errors
// logged (non-atomic fallback for external backends).
func (c *Client) takeState(ctx context.Context, state string) (StateEntry, error) {
	if taker, ok := c.state.(stateTaker); ok {
		return taker.Take(ctx, state)
	}
	entry, err := c.state.Get(ctx, state)
	if err != nil {
		return StateEntry{}, err
	}
	if delErr := c.state.Delete(ctx, state); delErr != nil {
		c.logger.ErrorContext(ctx, "oauth2: state.Delete failed after Get; entry may be replayable",
			redact.Error(delErr))
	}
	return entry, nil
}

// SessionFromRequest loads the session bound to the request's session
// cookie. Returns ErrSessionNotFound when the cookie is absent/empty or
// the store has no matching live session.
func (c *Client) SessionFromRequest(ctx context.Context, r *http.Request) (Session, error) {
	if r == nil {
		return Session{}, ErrSessionNotFound
	}
	cookie, err := r.Cookie(c.cookieName)
	if err != nil || cookie.Value == "" {
		return Session{}, ErrSessionNotFound
	}
	return c.sessions.Get(ctx, cookie.Value)
}

// CookieName returns the session cookie name used by this client.
func (c *Client) CookieName() string { return c.cookieName }

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

// stateCookieName is the browser-bound CSRF state cookie. Derived from
// the session cookie name so WithCookieName stays a single control.
func (c *Client) stateCookieName() string {
	return c.cookieName + "_state"
}

// stateBindingValue returns the cookie value for a login state token.
// We store a hash rather than the raw state so a leaked cookie alone
// does not give an attacker the full state parameter without also
// controlling the redirect query.
func stateBindingValue(state string) string {
	sum := sha256.Sum256([]byte(state))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (c *Client) stateCookie(value string) *http.Cookie {
	return &http.Cookie{
		Name:     c.stateCookieName(),
		Value:    value,
		Path:     "/",
		Domain:   c.cookieDomain,
		HttpOnly: true,
		Secure:   c.cookieSecure,
		// Lax so the cookie is sent on the top-level IdP redirect back
		// to /callback (SameSite=Strict would drop it on that hop).
		SameSite: http.SameSiteLaxMode,
		// MaxAge mirrors stateTTL; Expires is belt-and-braces for older
		// clients that ignore MaxAge.
		MaxAge:  int(c.stateTTL.Seconds()),
		Expires: time.Now().Add(c.stateTTL),
	}
}

func (c *Client) clearStateCookie() *http.Cookie {
	return &http.Cookie{
		Name:     c.stateCookieName(),
		Value:    "",
		Path:     "/",
		Domain:   c.cookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

// OAuth2Config returns a shallow copy of the underlying *xoauth2.Config
// so callers can build refresh-token transports
// (oauth2.Token{RefreshToken: ...}.Client(ctx)) or per-request OAuth2
// transports without re-discovering endpoints. Mutating the returned
// value never affects the client's login/callback exchange path.
func (c *Client) OAuth2Config() *xoauth2.Config {
	if c == nil || c.oauth == nil {
		return nil
	}
	cp := *c.oauth
	if c.oauth.Scopes != nil {
		cp.Scopes = append([]string(nil), c.oauth.Scopes...)
	}
	return &cp
}

// Provider returns the underlying *oidc.Provider for callers needing
// UserInfo or non-standard discovery fields.
func (c *Client) Provider() *oidc.Provider { return c.provider }
