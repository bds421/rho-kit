package oauth2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// LogoutResponse is the JSON body POST /oauth/logout returns when
// [WithRPInitiatedLogout] is enabled. The browser navigates to LogoutURL to
// end the provider session as well.
type LogoutResponse struct {
	LogoutURL string `json:"logout_url"`
}

// WithRPInitiatedLogout enables OpenID Connect RP-Initiated Logout. After
// the local session is deleted, POST /oauth/logout answers 200 with a JSON
// [LogoutResponse] instead of 204. Its logout_url is the provider's
// end_session_endpoint with client_id, id_token_hint (the session's stored
// ID token, when known) and post_logout_redirect_uri (when
// postLogoutRedirectURI is non-empty; it must be registered with the
// provider). The route stays POST-only so a cross-site GET cannot log users
// out; the frontend navigates to logout_url itself.
//
// [NewClient] fails when the provider does not advertise
// end_session_endpoint. Panics when postLogoutRedirectURI is non-empty but
// not an absolute http(s) URL.
func WithRPInitiatedLogout(postLogoutRedirectURI string) Option {
	if postLogoutRedirectURI != "" {
		u, err := url.Parse(postLogoutRedirectURI)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			panic("oauth2: WithRPInitiatedLogout requires an absolute http(s) post-logout redirect URI")
		}
	}
	return func(cl *Client) {
		cl.rpLogout = true
		cl.postLogoutRedirect = postLogoutRedirectURI
	}
}

// discoverEndSession reads end_session_endpoint from the discovery document.
func discoverEndSession(provider *oidc.Provider) (string, error) {
	var meta struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&meta); err != nil || meta.EndSessionEndpoint == "" {
		return "", errors.New("oauth2: WithRPInitiatedLogout requires the issuer to advertise end_session_endpoint")
	}
	u, err := url.Parse(meta.EndSessionEndpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("oauth2: issuer end_session_endpoint is not an absolute http(s) URL")
	}
	return meta.EndSessionEndpoint, nil
}

// logoutIDTokenHint returns the stored ID token for sessionID, or "" when
// the session is unknown or the store fails (logout proceeds without hint).
func (c *Client) logoutIDTokenHint(ctx context.Context, sessionID string) string {
	sess, err := c.sessions.Get(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, ErrSessionNotFound) {
			c.logger.WarnContext(ctx, "oauth2: session.Get failed on logout; continuing without id_token_hint", redact.Error(err))
		}
		return ""
	}
	if sess.IDToken == nil || sess.IDToken.IsEmpty() {
		return ""
	}
	return sess.IDToken.RevealString()
}

// endSessionURL builds the provider logout URL.
func (c *Client) endSessionURL(idTokenHint string) string {
	u, err := url.Parse(c.endSessionEndpoint)
	if err != nil {
		// Validated in discoverEndSession; unreachable.
		return c.endSessionEndpoint
	}
	q := u.Query()
	q.Set("client_id", c.oauth.ClientID)
	if idTokenHint != "" {
		q.Set("id_token_hint", idTokenHint)
	}
	if c.postLogoutRedirect != "" {
		q.Set("post_logout_redirect_uri", c.postLogoutRedirect)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) writeLogoutURL(w http.ResponseWriter, idTokenHint string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(LogoutResponse{LogoutURL: c.endSessionURL(idTokenHint)})
}
