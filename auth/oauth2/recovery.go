package oauth2

import (
	"log/slog"
	"net/http"
	"net/url"
)

// retryWindow is how long a transparent sign-in restart is remembered. A
// second failed callback inside it shows the error page instead of looping
// between the application and the identity provider.
const retryWindow = 2 * 60

func (c *Client) retryCookieName() string {
	return c.cookieName + "_retry"
}

// recoverSignIn answers a callback that cannot complete because its pending
// sign-in is unknown, unbound to this browser, or expired. No code is
// exchanged. A browser that already holds a valid session goes to returnTo;
// otherwise the sign-in restarts once with the same return URL.
func (c *Client) recoverSignIn(w http.ResponseWriter, r *http.Request, returnTo, reason string) {
	returnTo = c.safeReturnTo(returnTo)
	if _, err := c.SessionFromRequest(r.Context(), r); err == nil {
		c.logger.InfoContext(r.Context(), "oauth2: callback without pending sign-in, session already valid",
			slog.String("reason", reason))
		http.SetCookie(w, c.expiredCookie(c.retryCookieName(), c.flowCookiePath()))
		http.Redirect(w, r, returnTo, http.StatusFound)
		return
	}
	if retry, err := r.Cookie(c.retryCookieName()); err == nil && retry.Value != "" {
		c.logger.WarnContext(r.Context(), "oauth2: sign-in restart failed again", slog.String("reason", reason))
		http.SetCookie(w, c.expiredCookie(c.retryCookieName(), c.flowCookiePath()))
		c.renderFailure(w, r, http.StatusBadRequest, failureSignIn, returnTo)
		return
	}
	c.logger.InfoContext(r.Context(), "oauth2: restarting sign-in", slog.String("reason", reason))
	http.SetCookie(w, &http.Cookie{
		Name:     c.retryCookieName(),
		Value:    "1",
		Path:     c.flowCookiePath(),
		Domain:   c.cookieDomain,
		MaxAge:   retryWindow,
		HttpOnly: true,
		Secure:   c.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, c.loginURL(returnTo), http.StatusFound)
}

func (c *Client) safeReturnTo(target string) string {
	if safe, ok := safeRelativeRedirect(target); ok {
		return safe
	}
	return c.homePath
}

func (c *Client) loginURL(returnTo string) string {
	return c.pathPrefix + "/login?" + url.Values{"redirect_to": {returnTo}}.Encode()
}
