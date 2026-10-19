package oauth2

import (
	"context"
	"crypto/subtle"
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

// defaultRefreshLockWait is how long a refresh waits for another replica
// holding the session's refresh lock ([RefreshLocker]) before it treats the
// refresh as a transient failure.
const defaultRefreshLockWait = 5 * time.Second

// refreshLockPoll is the interval at which a waiting refresh re-reads the
// session and retries the lock.
const refreshLockPoll = 50 * time.Millisecond

// errRefreshLockTimeout reports that another replica held the refresh lock
// for longer than the configured wait.
var errRefreshLockTimeout = errors.New("oauth2: timed out waiting for the session refresh lock")

// WithTokenRefresh makes [Client.SessionFromRequest] (and the app/oidc
// middleware built on it) refresh a session's access token when it expires
// within leeway and the session holds a refresh token. Default: off, so
// sessions are returned exactly as stored.
//
// Behaviour when enabled:
//   - Refreshes are single-flight per session within the process; the stored
//     session is re-read first, so a refresh another replica already
//     persisted is reused instead of repeated. A store implementing
//     [RefreshLocker] (auth/oauth2/redis) also serializes refreshes across
//     replicas; a replica that finds the lock taken waits up to
//     [WithRefreshLockWait] for the other replica's tokens.
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
//     request; once it has expired (plus [WithRefreshOutageGrace]) the
//     session is treated as expired.
//   - Sessions without a refresh token or without a known expiry are not
//     touched.
//   - The write is a compare-and-set when the store implements
//     [SessionUpdater] (the memory and auth/oauth2/redis stores do): a
//     session deleted by logout, or rewritten, while the refresh was in
//     flight is not recreated; the refreshed tokens are discarded and the
//     request is unauthenticated. Other stores re-check existence before
//     Put, which narrows but does not close that race.
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

// WithRefreshOutageGrace keeps a session usable for up to d past its access
// token's expiry while token refresh fails transiently (a transport error or
// a 5xx token-endpoint response), so a short provider outage does not log
// every user out. The returned session then carries the expired access
// token; callers that forward it to resource servers see those reject it.
// Each request during the grace period retries the refresh. A provider
// rejection (4xx) or an invalid refreshed ID token still ends the session
// immediately, and the absolute lifetime and idle timeout still apply.
// Default: 0 (the session ends once the access token has expired).
// Only effective with [WithTokenRefresh].
//
// Panics on a negative duration.
func WithRefreshOutageGrace(d time.Duration) Option {
	if d < 0 {
		panic("oauth2: WithRefreshOutageGrace requires a non-negative duration")
	}
	return func(cl *Client) { cl.refreshOutageGrace = d }
}

// WithRefreshLockWait sets how long a token refresh waits while another
// replica holds the session's refresh lock (see [RefreshLocker]) before it
// gives up and treats the refresh as a transient failure (default 5s). While
// waiting it re-reads the session and uses the other replica's refreshed
// tokens as soon as they are stored. Panics on a non-positive duration.
func WithRefreshLockWait(d time.Duration) Option {
	if d <= 0 {
		panic("oauth2: WithRefreshLockWait requires a positive duration")
	}
	return func(cl *Client) { cl.refreshLockWait = d }
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
	if locker, ok := c.sessions.(RefreshLocker); ok {
		latest, release, err := c.lockRefresh(ctx, locker, sessionID)
		switch {
		case errors.Is(err, ErrSessionNotFound):
			return Session{}, err
		case err != nil:
			return c.refreshFailed(ctx, sessionID, current, err, false)
		}
		if release == nil {
			// Another replica stored refreshed tokens while we waited.
			return latest, nil
		}
		defer c.releaseRefreshLock(ctx, release)
		current = latest
		if !c.needsRefresh(current, time.Now()) {
			return current, nil
		}
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
		if sid := stringClaim(claims, "sid"); sid != "" {
			updated.SID = sid
		}
	}
	updated.Version = current.Version + 1
	return c.storeRefreshed(ctx, sessionID, current, updated)
}

// maxActivityMergeAttempts bounds how often a refreshed session is re-applied
// onto a session whose only concurrent change was recorded activity.
const maxActivityMergeAttempts = 3

// lockRefresh takes the cross-replica refresh lock for sessionID. It returns
// the session as stored after acquisition plus the release function, or a
// nil release with the session another replica refreshed while this one
// waited. ErrSessionNotFound means the session was deleted meanwhile.
func (c *Client) lockRefresh(ctx context.Context, locker RefreshLocker, sessionID string) (Session, func(context.Context) error, error) {
	deadline := time.Now().Add(c.refreshLockWait)
	for {
		release, acquired, err := locker.TryLockRefresh(ctx, sessionID, refreshTimeout)
		if err != nil {
			return Session{}, nil, err
		}
		if acquired {
			// Re-read: the previous holder may have finished just before.
			latest, err := c.sessions.Get(ctx, sessionID)
			if err != nil {
				c.releaseRefreshLock(ctx, release)
				return Session{}, nil, err
			}
			return latest, release, nil
		}
		timer := time.NewTimer(refreshLockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Session{}, nil, ctx.Err()
		case <-timer.C:
		}
		latest, err := c.sessions.Get(ctx, sessionID)
		if err != nil {
			return Session{}, nil, err
		}
		if !c.needsRefresh(latest, time.Now()) {
			return latest, nil, nil
		}
		if !time.Now().Before(deadline) {
			return Session{}, nil, errRefreshLockTimeout
		}
	}
}

// releaseRefreshLock frees a refresh lock, detached from request
// cancellation; a failure only delays the next refresh until the lock TTL.
func (c *Client) releaseRefreshLock(ctx context.Context, release func(context.Context) error) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := release(releaseCtx); err != nil {
		c.logger.WarnContext(ctx, "oauth2: releasing the session refresh lock failed", redact.Error(err))
	}
}

// storeRefreshed persists a refreshed session. With a [SessionUpdater] the
// write is a compare-and-set against the version that was refreshed: if the
// session was deleted (logout) or rewritten meanwhile, the refreshed tokens
// are discarded so a logout can never be undone. A concurrent write that
// only recorded activity ([WithIdleTimeout]) still holds the refresh token
// that was redeemed; the refreshed tokens are then re-applied onto it. Other
// stores fall back to re-checking existence before Put (a narrower,
// documented race).
func (c *Client) storeRefreshed(ctx context.Context, sessionID string, current, updated Session) (Session, error) {
	ttl := c.sessionTTLFor(updated, time.Now())
	if ttl <= 0 {
		return Session{}, c.expireSession(ctx, sessionID, "lifetime reached during token refresh")
	}
	if updater, ok := c.sessions.(SessionUpdater); ok {
		expectedVersion := current.Version
		for attempt := 0; ; attempt++ {
			stored, err := updater.Update(ctx, sessionID, expectedVersion, updated, ttl)
			if err != nil {
				c.logger.WarnContext(ctx, "oauth2: session.Update failed after token refresh", redact.Error(err))
				return Session{}, err
			}
			if stored {
				return updated, nil
			}
			// Lost the race. A concurrent refresh that persisted fresh tokens
			// is still a valid session; a deleted or otherwise changed one is
			// not.
			latest, err := c.sessions.Get(ctx, sessionID)
			if err == nil && latest.Version > expectedVersion && !c.needsRefresh(latest, time.Now()) {
				return latest, nil
			}
			if err != nil || attempt+1 >= maxActivityMergeAttempts || !onlyActivityChanged(current, latest) {
				break
			}
			if latest.LastActivity.After(updated.LastActivity) {
				updated.LastActivity = latest.LastActivity
			}
			expectedVersion = latest.Version
			updated.Version = latest.Version + 1
		}
		c.logger.WarnContext(ctx, "oauth2: session changed during token refresh; discarding refreshed tokens")
		return Session{}, ErrSessionNotFound
	}
	if _, err := c.sessions.Get(ctx, sessionID); err != nil {
		// Deleted (logout) or unreadable while refreshing: do not recreate.
		return Session{}, err
	}
	if err := c.sessions.Put(ctx, sessionID, updated, ttl); err != nil {
		c.logger.WarnContext(ctx, "oauth2: session.Put failed after token refresh", redact.Error(err))
		return Session{}, err
	}
	return updated, nil
}

// onlyActivityChanged reports whether latest is the refreshed session as it
// was read (same subject and refresh token), i.e. a concurrent write did not
// rotate its tokens.
func onlyActivityChanged(refreshed, latest Session) bool {
	return latest.UserID == refreshed.UserID && sameSecret(latest.RefreshToken, refreshed.RefreshToken)
}

func sameSecret(a, b *secret.String) bool {
	if a == nil || b == nil {
		return a == b
	}
	return subtle.ConstantTimeCompare([]byte(a.RevealString()), []byte(b.RevealString())) == 1
}

// refreshFailed decides whether a failed refresh expires the session. A
// permanent failure (provider rejection, invalid ID token) always does; a
// transient one only once the current access token has expired and the
// [WithRefreshOutageGrace] period after that has passed.
func (c *Client) refreshFailed(ctx context.Context, sessionID string, current Session, cause error, permanent bool) (Session, error) {
	// A rotating refresh token may already have been redeemed by another
	// replica, which then persisted the new tokens: use them.
	if latest, err := c.sessions.Get(ctx, sessionID); err == nil && !c.needsRefresh(latest, time.Now()) {
		return latest, nil
	}
	if !permanent && time.Now().Before(current.Expiry.Add(c.refreshOutageGrace)) {
		c.logger.WarnContext(ctx, "oauth2: token refresh failed; serving session within its validity or outage grace", redact.Error(cause))
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
