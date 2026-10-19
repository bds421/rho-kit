package oauth2

import (
	"context"
	"errors"
	"net/http"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// ErrSignInRefused is what a [WithSignInHook] function returns (or wraps) to
// refuse a sign-in on purpose, for example for a suspended account. The
// browser receives 403; any other hook error is treated as a failure to
// complete the sign-in and answered with 502.
var ErrSignInRefused = errors.New("oauth2: sign-in refused")

// WithSignInHook runs fn in the login callback after the ID token is verified
// and before the session is stored. fn sees the new session, including the
// raw ID token, and returns the attributes kept on it ([Session.Attributes]).
// When fn fails no session is created.
//
// Use it to resolve the person in the application's own directory with the
// fresh, verified token, once per sign-in instead of on every request.
func WithSignInHook(fn func(ctx context.Context, sess Session) (map[string]string, error)) Option {
	return func(c *Client) { c.signInHook = fn }
}

func (c *Client) runSignInHook(w http.ResponseWriter, r *http.Request, sess *Session) bool {
	if c.signInHook == nil {
		return true
	}
	attrs, err := c.signInHook(r.Context(), *sess)
	switch {
	case errors.Is(err, ErrSignInRefused):
		c.logger.InfoContext(r.Context(), "oauth2: sign-in refused by hook")
		http.Error(w, ErrSignInRefused.Error(), http.StatusForbidden)
		return false
	case err != nil:
		c.logger.WarnContext(r.Context(), "oauth2: sign-in hook failed", redact.Error(err))
		http.Error(w, "oauth2: sign-in could not be completed", http.StatusBadGateway)
		return false
	}
	sess.Attributes = attrs
	return true
}
