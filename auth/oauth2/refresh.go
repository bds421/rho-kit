package oauth2

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	xoauth2 "golang.org/x/oauth2"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/secret"
)

// refreshTimeout bounds one shared refresh (token endpoint + store writes).
// The refresh is detached from the triggering request so one canceled
// request cannot fail every caller waiting on the same session.
const refreshTimeout = 30 * time.Second

// WithTokenRefresh makes [Client.SessionFromRequest] (and the app/oidc
// middleware built on it) refresh a session's access token when it expires
// within leeway and the session holds a refresh token. Default: off, so
// sessions are returned exactly as stored.
//
// Behaviour when enabled:
//   - Refreshes are single-flight per session within the process; the stored
//     session is re-read first, so a refresh another replica already
//     persisted is reused instead of repeated.
//   - The refreshed tokens replace the stored ones and the session TTL is
//     reset (sliding while the provider keeps issuing tokens). A refresh
//     response without a new refresh token keeps the old one.
//   - A returned ID token is verified like the login ID token and must carry
//     the same subject; its claims then replace the session claims.
//   - When the provider rejects the refresh (a 4xx token-endpoint response
//     such as invalid_grant) or the refreshed ID token is invalid, the
//     session is deleted and [ErrSessionNotFound] is returned. A transport
//     failure or 5xx response while the current access token is still
//     unexpired keeps serving the current session and retries on the next
//     request; once it has expired the session is treated as expired.
//   - Sessions without a refresh token or without a known expiry are not
//     touched.
//
// Panics on a negative leeway.
func WithTokenRefresh(leeway time.Duration) Option {
	if leeway < 0 {
		panic("oauth2: WithTokenRefresh requires a non-negative leeway")
	}
	return func(cl *Client) {
		cl.refreshEnabled = true
		cl.refreshLeeway = leeway
	}
}

// needsRefresh reports whether sess should be refreshed at now.
func (c *Client) needsRefresh(sess Session, now time.Time) bool {
	return c.refreshEnabled &&
		sess.RefreshToken != nil && !sess.RefreshToken.IsEmpty() &&
		!sess.Expiry.IsZero() &&
		!now.Add(c.refreshLeeway).Before(sess.Expiry)
}

// refreshIfNeeded returns sess unchanged unless it needs a refresh, in which
// case it joins (or starts) the single in-process refresh for sessionID.
func (c *Client) refreshIfNeeded(ctx context.Context, sessionID string, sess Session) (Session, error) {
	if !c.needsRefresh(sess, time.Now()) {
		return sess, nil
	}
	v, err, _ := c.refreshGroup.Do(sessionID, func() (any, error) {
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		defer cancel()
		return c.refreshSession(refreshCtx, sessionID)
	})
	if err != nil {
		return Session{}, err
	}
	// Each waiter gets its own secret buffers so one caller zeroing its copy
	// cannot corrupt another's.
	return cloneSession(v.(Session)), nil
}

// refreshSession performs one refresh for sessionID against the stored state.
func (c *Client) refreshSession(ctx context.Context, sessionID string) (Session, error) {
	current, err := c.sessions.Get(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	if !c.needsRefresh(current, time.Now()) {
		// Refreshed meanwhile (another flight or replica).
		return current, nil
	}
	exchangeCtx := oidc.ClientContext(ctx, c.httpClient)
	token, err := c.oauth.TokenSource(exchangeCtx, &xoauth2.Token{
		RefreshToken: current.RefreshToken.RevealString(),
	}).Token()
	if err != nil {
		return c.refreshFailed(ctx, sessionID, current, err, refreshRejected(err))
	}

	updated := current
	updated.AccessToken = secret.NewFromString(token.AccessToken)
	updated.Expiry = token.Expiry
	if token.RefreshToken != "" {
		updated.RefreshToken = secret.NewFromString(token.RefreshToken)
	}
	if rawIDToken, ok := token.Extra("id_token").(string); ok && rawIDToken != "" {
		idToken, err := c.verifier.Verify(exchangeCtx, rawIDToken)
		if err != nil {
			return c.refreshFailed(ctx, sessionID, current, err, true)
		}
		if idToken.Subject != current.UserID {
			return c.refreshFailed(ctx, sessionID, current, errors.New("refreshed id_token subject changed"), true)
		}
		var claims map[string]any
		if err := idToken.Claims(&claims); err != nil {
			return c.refreshFailed(ctx, sessionID, current, err, true)
		}
		updated.Claims = claims
		updated.IDToken = secret.NewFromString(rawIDToken)
	}
	if err := c.sessions.Put(ctx, sessionID, updated, c.sessionTTL); err != nil {
		c.logger.WarnContext(ctx, "oauth2: session.Put failed after token refresh", redact.Error(err))
		return Session{}, err
	}
	return updated, nil
}

// refreshFailed decides whether a failed refresh expires the session. A
// permanent failure (provider rejection, invalid ID token) always does; a
// transient one only once the current access token has expired.
func (c *Client) refreshFailed(ctx context.Context, sessionID string, current Session, cause error, permanent bool) (Session, error) {
	// A rotating refresh token may already have been redeemed by another
	// replica, which then persisted the new tokens: use them.
	if latest, err := c.sessions.Get(ctx, sessionID); err == nil && !c.needsRefresh(latest, time.Now()) {
		return latest, nil
	}
	if !permanent && time.Now().Before(current.Expiry) {
		c.logger.WarnContext(ctx, "oauth2: token refresh failed; serving unexpired session", redact.Error(cause))
		return current, nil
	}
	c.logger.WarnContext(ctx, "oauth2: token refresh failed; session expired",
		redact.Error(cause), slog.Bool("rejected", permanent))
	if err := c.sessions.Delete(ctx, sessionID); err != nil {
		c.logger.ErrorContext(ctx, "oauth2: session.Delete failed after refresh failure", redact.Error(err))
	}
	return Session{}, ErrSessionNotFound
}

// refreshRejected reports whether the token endpoint definitively refused
// the refresh (a 4xx OAuth2 error such as invalid_grant). Transport errors
// and 5xx responses are transient.
func refreshRejected(err error) bool {
	var re *xoauth2.RetrieveError
	if !errors.As(err, &re) {
		return false
	}
	return re.Response == nil || re.Response.StatusCode < http.StatusInternalServerError
}
