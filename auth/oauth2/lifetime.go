package oauth2

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/core/v2/redact"
)

// DefaultBackgroundRequestHeader is the request header that marks a request
// as not user-initiated for [WithIdleTimeout]. A value of "1" or "true"
// (case-insensitive) sets the mark. Override the name with
// [WithBackgroundRequestHeader].
const DefaultBackgroundRequestHeader = "X-Background-Request"

// activityWriteInterval bounds how often a user-initiated request rewrites
// Session.LastActivity: at most one store write per session per interval.
const activityWriteInterval = time.Minute

var backgroundRequestKey = contextutil.NewKey[bool]("auth.oauth2.background_request")

// WithBackgroundRequest marks ctx as belonging to a request the user did not
// initiate (polling, realtime token renewal, prefetch). Such requests are
// still authenticated and may refresh tokens, but they do not extend the
// [WithIdleTimeout] window. Middleware that recognises background traffic by
// route can set the mark before the app/oidc middleware or
// [Client.SessionFromRequest] runs.
func WithBackgroundRequest(ctx context.Context) context.Context {
	return backgroundRequestKey.Set(ctx, true)
}

// IsBackgroundRequest reports whether ctx carries the [WithBackgroundRequest]
// mark.
func IsBackgroundRequest(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, ok := backgroundRequestKey.Get(ctx)
	return ok && v
}

// WithAbsoluteSessionLifetime caps every session at d after its creation
// (Session.CreatedAt), regardless of activity or token refresh. Token refresh
// and the sliding store TTL never extend a session beyond CreatedAt+d, and
// the session cookie expires no later than that. Once the cap is reached the
// session is deleted and [Client.SessionFromRequest] returns
// [ErrSessionNotFound] (the user is logged out). Default: off.
//
// Sessions without CreatedAt (stored before it was recorded) are treated as
// expired while the option is set, so enabling it logs those users out once.
//
// Panics on a non-positive duration.
func WithAbsoluteSessionLifetime(d time.Duration) Option {
	if d <= 0 {
		panic("oauth2: WithAbsoluteSessionLifetime requires a positive duration")
	}
	return func(cl *Client) { cl.absoluteLifetime = d }
}

// WithIdleTimeout ends a session when no user-initiated request was seen for
// d. Each user-initiated [Client.SessionFromRequest] records
// Session.LastActivity, writing it at most once per minute, so the timeout
// is accurate to one minute and may end a session up to a minute early.
// Requests marked with [WithBackgroundRequest] or the
// [DefaultBackgroundRequestHeader] header (see [WithBackgroundRequestHeader])
// neither record activity nor extend the window, though they may still
// refresh tokens. The store TTL is capped at LastActivity+d so idle sessions
// are also reclaimed by the store. Default: off.
//
// The header is set by the client application, so it can only shorten that
// client's own session; it confers no authority.
//
// Sessions without LastActivity fall back to CreatedAt; sessions with
// neither are treated as expired while the option is set.
//
// Panics on a non-positive duration.
func WithIdleTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("oauth2: WithIdleTimeout requires a positive duration")
	}
	return func(cl *Client) { cl.idleTimeout = d }
}

// WithBackgroundRequestHeader overrides the header name that marks a request
// as background traffic for [WithIdleTimeout] (default
// [DefaultBackgroundRequestHeader]). A value of "1" or "true"
// (case-insensitive) sets the mark. Panics when name is not a valid HTTP
// header field name.
func WithBackgroundRequestHeader(name string) Option {
	if !validHeaderName(name) {
		panic("oauth2: WithBackgroundRequestHeader requires a valid header name")
	}
	return func(cl *Client) { cl.backgroundHeader = http.CanonicalHeaderKey(name) }
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// isBackground reports whether r is marked as not user-initiated, by context
// or header.
func (c *Client) isBackground(ctx context.Context, r *http.Request) bool {
	if IsBackgroundRequest(ctx) || IsBackgroundRequest(r.Context()) {
		return true
	}
	v := strings.TrimSpace(r.Header.Get(c.backgroundHeader))
	return v == "1" || strings.EqualFold(v, "true")
}

// lastActivity returns the reference time for the idle timeout.
func lastActivity(sess Session) time.Time {
	if !sess.LastActivity.IsZero() {
		return sess.LastActivity
	}
	return sess.CreatedAt
}

// lifetimeExceeded reports which configured limit, if any, sess has reached
// at now.
func (c *Client) lifetimeExceeded(sess Session, now time.Time) (string, bool) {
	if c.absoluteLifetime > 0 && (sess.CreatedAt.IsZero() || !now.Before(sess.CreatedAt.Add(c.absoluteLifetime))) {
		return "absolute lifetime", true
	}
	if c.idleTimeout > 0 {
		last := lastActivity(sess)
		if last.IsZero() || !now.Before(last.Add(c.idleTimeout)) {
			return "idle timeout", true
		}
	}
	return "", false
}

// sessionTTLFor returns the store TTL for sess written at now: the session
// TTL, capped by the absolute lifetime and the idle window. A non-positive
// result means the session must not be stored.
func (c *Client) sessionTTLFor(sess Session, now time.Time) time.Duration {
	ttl := c.sessionTTL
	if c.absoluteLifetime > 0 {
		ttl = min(ttl, sess.CreatedAt.Add(c.absoluteLifetime).Sub(now))
	}
	if c.idleTimeout > 0 {
		ttl = min(ttl, lastActivity(sess).Add(c.idleTimeout).Sub(now))
	}
	return ttl
}

// expireSession deletes a session that reached a lifetime limit.
func (c *Client) expireSession(ctx context.Context, sessionID, reason string) error {
	c.logger.InfoContext(ctx, "oauth2: session ended", slog.String("reason", reason))
	if err := c.sessions.Delete(ctx, sessionID); err != nil {
		c.logger.ErrorContext(ctx, "oauth2: session.Delete failed for an ended session", redact.Error(err))
	}
	return ErrSessionNotFound
}

// recordActivity stores now as the session's LastActivity when the idle
// timeout is enabled and the recorded value is at least
// activityWriteInterval old. Write failures and lost compare-and-set races
// are not fatal: the request proceeds and the next one retries.
func (c *Client) recordActivity(ctx context.Context, sessionID string, sess Session, now time.Time) Session {
	if c.idleTimeout <= 0 || now.Sub(lastActivity(sess)) < activityWriteInterval {
		return sess
	}
	updated := sess
	updated.LastActivity = now
	updated.Version = sess.Version + 1
	ttl := c.sessionTTLFor(updated, now)
	if ttl <= 0 {
		return sess
	}
	if updater, ok := c.sessions.(SessionUpdater); ok {
		stored, err := updater.Update(ctx, sessionID, sess.Version, cloneSession(updated), ttl)
		if err != nil {
			c.logger.WarnContext(ctx, "oauth2: recording session activity failed", redact.Error(err))
			return sess
		}
		if !stored {
			return sess
		}
		return updated
	}
	if _, err := c.sessions.Get(ctx, sessionID); err != nil {
		return sess
	}
	if err := c.sessions.Put(ctx, sessionID, cloneSession(updated), ttl); err != nil {
		c.logger.WarnContext(ctx, "oauth2: recording session activity failed", redact.Error(err))
		return sess
	}
	return updated
}
