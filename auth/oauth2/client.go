package oauth2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/secret"
)

// Client is the kit's OAuth2/OIDC relying-party client. Construct with
// [NewClient]. Concurrency-safe after construction.
type Client struct {
	cfg          Config
	meta         providerMetadata
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

// WithHTTPClient overrides the http.Client used for issuer discovery,
// token-endpoint exchange, and userinfo. Defaults to a client with
// 10s timeout and TLS-12 minimum.
func WithHTTPClient(c *http.Client) Option {
	if c == nil {
		panic("oauth2: WithHTTPClient requires non-nil client")
	}
	return func(cl *Client) { cl.httpClient = c }
}

// WithSessionStore wires the persistence backend for logged-in
// sessions. Required for a usable client (the handlers panic on
// callback without one).
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
// with a client secret on providers that don't support PKCE — most
// modern providers do. Discouraged.
func WithoutPKCE() Option {
	return func(cl *Client) { cl.usePKCE = false }
}

// WithSessionTTL overrides the session-store TTL. Default 24h.
func WithSessionTTL(d time.Duration) Option {
	if d <= 0 {
		panic("oauth2: WithSessionTTL requires positive duration")
	}
	return func(cl *Client) { cl.sessionTTL = d }
}

// WithStateTTL overrides the in-flight login state TTL. Default 10m
// (a user typing in MFA shouldn't expire the login).
func WithStateTTL(d time.Duration) Option {
	if d <= 0 {
		panic("oauth2: WithStateTTL requires positive duration")
	}
	return func(cl *Client) { cl.stateTTL = d }
}

// WithCookieName overrides the session cookie name. Default
// "kit_oauth_session".
func WithCookieName(name string) Option {
	if name == "" {
		panic("oauth2: WithCookieName requires non-empty name")
	}
	return func(cl *Client) { cl.cookieName = name }
}

// WithCookieDomain restricts the session cookie to a domain. Optional.
func WithCookieDomain(d string) Option {
	return func(cl *Client) { cl.cookieDomain = d }
}

// WithInsecureCookie disables the Secure cookie attribute for local
// development over plain HTTP. Production should always serve over
// TLS so the default Secure=true wins.
func WithInsecureCookie() Option {
	return func(cl *Client) { cl.cookieSecure = false }
}

// NewClient constructs a Client. Fetches /.well-known/openid-configuration
// from cfg.Issuer at construction time (returns ErrIssuerDiscovery on
// failure so the service fails fast at startup rather than at first
// login).
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
	if _, err := url.Parse(cfg.RedirectURL); err != nil {
		return nil, fmt.Errorf("oauth2: invalid RedirectURL: %w", err)
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
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	// Ensure openid scope (required by OIDC).
	if !containsScope(cfg.Scopes, "openid") {
		c.cfg.Scopes = append([]string{"openid"}, cfg.Scopes...)
	}
	meta, err := discoverProvider(ctx, c.httpClient, cfg.Issuer)
	if err != nil {
		return nil, err
	}
	c.meta = meta
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

// Handlers returns the http.Handler that serves /login, /callback,
// /logout under a path prefix. Mount with mux.Handle(prefix+"/", ...).
//
// The default handler paths are:
//
//	/login    GET  — redirect to issuer
//	/callback GET  — exchange code, set session cookie
//	/logout   POST — drop session, optionally redirect to end_session_endpoint
//
// The handlers strip the prefix when routing, so mounting under
// /oauth/ works.
func (c *Client) Handlers() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", c.handleLogin)
	mux.HandleFunc("GET /callback", c.handleCallback)
	mux.HandleFunc("POST /logout", c.handleLogout)
	return http.StripPrefix("/oauth", mux)
}

// handleLogin generates state + (optional) PKCE verifier, persists
// them, and redirects to the issuer's authorization endpoint.
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
	if redirectTo := r.URL.Query().Get("redirect_to"); redirectTo != "" {
		entry.RedirectTo = redirectTo
	}

	params := url.Values{
		"response_type": {"code"},
		"client_id":     {c.cfg.ClientID},
		"redirect_uri":  {c.cfg.RedirectURL},
		"scope":         {strings.Join(c.cfg.Scopes, " ")},
		"state":         {state},
		"nonce":         {nonce},
	}
	if c.usePKCE {
		verifier, err := generateCodeVerifier()
		if err != nil {
			http.Error(w, "oauth2: pkce verifier failed", http.StatusInternalServerError)
			return
		}
		entry.CodeVerifier = verifier
		params.Set("code_challenge", codeChallengeS256(verifier))
		params.Set("code_challenge_method", "S256")
	}

	if err := c.state.Put(r.Context(), state, entry, c.stateTTL); err != nil {
		c.logger.WarnContext(r.Context(), "oauth2: state.Put failed", slog.String("error", err.Error()))
		http.Error(w, "oauth2: state persistence failed", http.StatusInternalServerError)
		return
	}

	target := c.meta.AuthorizationEndpoint + "?" + params.Encode()
	http.Redirect(w, r, target, http.StatusFound)
}

// handleCallback exchanges the authorization code for tokens, verifies
// the ID token's nonce, creates a session, and sets the cookie.
func (c *Client) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		desc := q.Get("error_description")
		http.Error(w, fmt.Sprintf("oauth2: provider error: %s (%s)", errParam, desc), http.StatusBadRequest)
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
	// Single-use: delete state before exchange so a replay can't be
	// repeated even on a slow code-exchange path.
	_ = c.state.Delete(r.Context(), stateToken)

	tokens, err := c.exchangeCode(r.Context(), code, entry.CodeVerifier)
	if err != nil {
		http.Error(w, ErrCodeExchange.Error(), http.StatusBadGateway)
		return
	}

	// ID-token verification (nonce + sub + exp) — we do a minimal
	// inline check here. Callers wanting deeper validation (claims
	// like aud/iss) should plug security/jwtutil.Provider into a
	// custom handler chain. The kit's default verifies nonce + that
	// sub exists; everything else is provider-specific.
	if err := verifyIDToken(tokens.IDToken, entry.Nonce); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sessionID, err := generateRandomToken()
	if err != nil {
		http.Error(w, "oauth2: session id generation failed", http.StatusInternalServerError)
		return
	}
	sess := Session{
		SessionID:    sessionID,
		UserID:       extractSubFromIDToken(tokens.IDToken),
		AccessToken:  secret.NewFromString(tokens.AccessToken),
		RefreshToken: secret.NewFromString(tokens.RefreshToken),
		Expiry:       time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second),
		Claims:       extractClaims(tokens.IDToken),
	}
	if err := c.sessions.Put(r.Context(), sessionID, sess, c.sessionTTL); err != nil {
		http.Error(w, "oauth2: session persistence failed", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, c.sessionCookie(sessionID))
	if entry.RedirectTo != "" {
		http.Redirect(w, r, entry.RedirectTo, http.StatusFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLogout drops the session and clears the cookie.
func (c *Client) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(c.cookieName)
	if err == nil {
		_ = c.sessions.Delete(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     c.cookieName,
		Value:    "",
		Path:     "/",
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

// tokenResponse mirrors the token-endpoint JSON shape.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
}

func (c *Client) exchangeCode(ctx context.Context, code, codeVerifier string) (tokenResponse, error) {
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {c.cfg.RedirectURL},
		"client_id":    {c.cfg.ClientID},
	}
	if c.cfg.ClientSecret != nil && !c.cfg.ClientSecret.IsEmpty() {
		form.Set("client_secret", c.cfg.ClientSecret.RevealString())
	}
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.meta.TokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%w: build request: %v", ErrCodeExchange, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%w: do: %v", ErrCodeExchange, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("%w: HTTP %d", ErrCodeExchange, resp.StatusCode)
	}
	var out tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return tokenResponse{}, fmt.Errorf("%w: decode: %v", ErrCodeExchange, err)
	}
	return out, nil
}
