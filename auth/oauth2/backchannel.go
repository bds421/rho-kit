package oauth2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// backChannelLogoutEvent is the events member that identifies a logout token
// (OpenID Connect Back-Channel Logout 1.0, section 2.4).
const backChannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

const (
	// logoutTokenMaxAge is the oldest iat accepted for a logout token. The
	// jti replay window covers it.
	logoutTokenMaxAge = 5 * time.Minute
	// logoutTokenClockSkew tolerates provider clocks slightly ahead.
	logoutTokenClockSkew = time.Minute
	// maxLogoutRequestBytes bounds the back-channel logout request body.
	maxLogoutRequestBytes = 64 << 10
	// maxJTILength bounds the jti claim.
	maxJTILength = 512
)

// WithBackChannelLogout enables OpenID Connect Back-Channel Logout 1.0.
// [Client.Handlers] then also serves POST /oauth/backchannel-logout, the URL
// to register with the provider as backchannel_logout_uri. The provider
// posts a logout_token (form-encoded); the client verifies it and deletes
// the matching sessions:
//
//   - signature against the provider's JWKS, iss, aud = client ID and exp,
//     via the same go-oidc verifier as the login ID token;
//   - iat present, at most five minutes old and not more than a minute in
//     the future;
//   - jti present and not seen before (see [WithReplayCache]);
//   - an events claim with the back-channel logout event as a JSON object;
//   - sid and/or sub present; nonce absent.
//
// With sid, the sessions created from that provider session are deleted
// (restricted to sub when both are present); with sub alone, all sessions of
// the subject. The handler answers 200 on success and 400
// {"error":"invalid_request"} for an invalid token. The jti is recorded
// before sessions are deleted, so a replayed token is rejected even if the
// deletion of the first delivery failed (answered 500).
//
// The route is called by the provider, not the browser: exempt it from CSRF
// and session middleware. [NewClient] fails unless the session store
// implements [SessionIndex].
func WithBackChannelLogout() Option {
	return func(cl *Client) { cl.backChannelLogout = true }
}

// WithReplayCache sets the cache that rejects replayed back-channel logout
// tokens. Default: the session store when it implements [ReplayCache]
// (auth/oauth2/redis does), otherwise an in-process [MemoryReplayCache],
// which is only correct for a single replica.
func WithReplayCache(cache ReplayCache) Option {
	if cache == nil {
		panic("oauth2: WithReplayCache requires a non-nil cache")
	}
	return func(cl *Client) { cl.replayCache = cache }
}

// WithSessionRevocationHook registers fn to run after sessions were deleted
// by [Client.DeleteSessionsBySubject] or back-channel logout. It is called
// once per affected subject, also when no stored session matched, so the
// application can close other channels authenticated for that subject
// (realtime connections, caches). It runs synchronously on the request
// goroutine and must not block for long. Panics on a nil fn.
func WithSessionRevocationHook(fn func(ctx context.Context, subject string)) Option {
	if fn == nil {
		panic("oauth2: WithSessionRevocationHook requires a non-nil function")
	}
	return func(cl *Client) { cl.revocationHook = fn }
}

// DeleteSessionsBySubject deletes every stored session of subject (the ID
// token "sub", Session.UserID), for example when the user's membership is
// removed, and returns how many were deleted. A login that completes
// concurrently can still create a new session; enforce the membership in
// authorization as well. The [WithSessionRevocationHook] hook runs with
// subject after a successful deletion. Returns [ErrSessionIndexUnsupported]
// when the session store does not implement [SessionIndex].
func (c *Client) DeleteSessionsBySubject(ctx context.Context, subject string) (int, error) {
	if subject == "" {
		return 0, errors.New("oauth2: DeleteSessionsBySubject requires a subject")
	}
	index, ok := c.sessions.(SessionIndex)
	if !ok {
		return 0, ErrSessionIndexUnsupported
	}
	ids, err := index.SessionIDsBySubject(ctx, subject)
	if err != nil {
		return 0, err
	}
	n, _, err := c.deleteMatching(ctx, ids, func(s Session) bool { return s.UserID == subject })
	if err != nil {
		return n, err
	}
	c.runRevocationHook(ctx, subject)
	return n, nil
}

// deleteMatching deletes the sessions among ids that still exist and match,
// returning the count and the distinct subjects deleted.
func (c *Client) deleteMatching(ctx context.Context, ids []string, match func(Session) bool) (int, []string, error) {
	var (
		n        int
		subjects []string
		seen     = map[string]bool{}
	)
	for _, id := range ids {
		sess, err := c.sessions.Get(ctx, id)
		if errors.Is(err, ErrSessionNotFound) {
			continue
		}
		if err != nil {
			return n, subjects, err
		}
		if !match(sess) {
			continue
		}
		if err := c.sessions.Delete(ctx, id); err != nil {
			return n, subjects, err
		}
		n++
		if !seen[sess.UserID] {
			seen[sess.UserID] = true
			subjects = append(subjects, sess.UserID)
		}
	}
	return n, subjects, nil
}

func (c *Client) runRevocationHook(ctx context.Context, subject string) {
	if c.revocationHook != nil && subject != "" {
		c.revocationHook(ctx, subject)
	}
}

// logoutToken is a verified back-channel logout token.
type logoutToken struct {
	issuer  string
	subject string
	sid     string
	jti     string
}

// verifyLogoutToken validates raw per OpenID Connect Back-Channel Logout 1.0
// section 2.6 except jti replay, which the caller records.
func (c *Client) verifyLogoutToken(ctx context.Context, raw string, now time.Time) (logoutToken, error) {
	tok, err := c.verifier.Verify(oidc.ClientContext(ctx, c.httpClient), raw)
	if err != nil {
		return logoutToken{}, fmt.Errorf("verify: %w", err)
	}
	var claims map[string]json.RawMessage
	if err := tok.Claims(&claims); err != nil {
		return logoutToken{}, fmt.Errorf("decode claims: %w", err)
	}
	if _, hasNonce := claims["nonce"]; hasNonce {
		return logoutToken{}, errors.New("nonce claim present")
	}
	if tok.IssuedAt.IsZero() {
		return logoutToken{}, errors.New("iat claim missing")
	}
	if tok.IssuedAt.After(now.Add(logoutTokenClockSkew)) {
		return logoutToken{}, errors.New("iat in the future")
	}
	if tok.IssuedAt.Before(now.Add(-logoutTokenMaxAge)) {
		return logoutToken{}, errors.New("iat too old")
	}
	var events map[string]json.RawMessage
	if err := json.Unmarshal(claims["events"], &events); err != nil || events == nil {
		return logoutToken{}, errors.New("events claim missing or not an object")
	}
	var event map[string]any
	if err := json.Unmarshal(events[backChannelLogoutEvent], &event); err != nil || event == nil {
		return logoutToken{}, errors.New("back-channel logout event missing")
	}
	var jti, sid string
	if raw, ok := claims["jti"]; ok {
		if err := json.Unmarshal(raw, &jti); err != nil {
			return logoutToken{}, errors.New("jti claim is not a string")
		}
	}
	if jti == "" || len(jti) > maxJTILength {
		return logoutToken{}, errors.New("jti claim missing or too long")
	}
	if raw, ok := claims["sid"]; ok {
		if err := json.Unmarshal(raw, &sid); err != nil {
			return logoutToken{}, errors.New("sid claim is not a string")
		}
	}
	if sid == "" && tok.Subject == "" {
		return logoutToken{}, errors.New("neither sid nor sub present")
	}
	return logoutToken{issuer: tok.Issuer, subject: tok.Subject, sid: sid, jti: jti}, nil
}

// replayKey scopes a jti to the issuer and hides it behind a hash so the
// cache key is fixed-size printable text.
func (t logoutToken) replayKey() string {
	sum := sha256.Sum256([]byte(t.issuer + "\x00" + t.jti))
	return "bcl:" + hex.EncodeToString(sum[:])
}

// handleBackChannelLogout serves POST /oauth/backchannel-logout.
func (c *Client) handleBackChannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, maxLogoutRequestBytes)
	if err := r.ParseForm(); err != nil {
		writeLogoutError(w, http.StatusBadRequest)
		return
	}
	raw := r.PostForm.Get("logout_token")
	if raw == "" {
		writeLogoutError(w, http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	tok, err := c.verifyLogoutToken(ctx, raw, time.Now())
	if err != nil {
		c.logger.WarnContext(ctx, "oauth2: back-channel logout token rejected", redact.Error(err))
		writeLogoutError(w, http.StatusBadRequest)
		return
	}
	first, err := c.replayCache.Remember(ctx, tok.replayKey(), logoutTokenMaxAge+logoutTokenClockSkew)
	if err != nil {
		c.logger.ErrorContext(ctx, "oauth2: back-channel logout replay check failed", redact.Error(err))
		http.Error(w, "oauth2: logout temporarily unavailable", http.StatusInternalServerError)
		return
	}
	if !first {
		c.logger.WarnContext(ctx, "oauth2: back-channel logout token replayed")
		writeLogoutError(w, http.StatusBadRequest)
		return
	}
	n, subjects, err := c.backChannelDelete(ctx, tok)
	if err != nil {
		c.logger.ErrorContext(ctx, "oauth2: back-channel logout session deletion failed", redact.Error(err))
		http.Error(w, "oauth2: logout failed", http.StatusInternalServerError)
		return
	}
	c.logger.InfoContext(ctx, "oauth2: back-channel logout", slog.Int("sessions", n))
	for _, subject := range subjects {
		c.runRevocationHook(ctx, subject)
	}
	w.WriteHeader(http.StatusOK)
}

// backChannelDelete deletes the sessions a verified logout token names and
// returns the subjects to notify.
func (c *Client) backChannelDelete(ctx context.Context, tok logoutToken) (int, []string, error) {
	index := c.sessions.(SessionIndex) // checked in NewClient
	var (
		ids   []string
		err   error
		match func(Session) bool
	)
	if tok.sid != "" {
		ids, err = index.SessionIDsBySID(ctx, tok.sid)
		match = func(s Session) bool { return s.SID == tok.sid && (tok.subject == "" || s.UserID == tok.subject) }
	} else {
		ids, err = index.SessionIDsBySubject(ctx, tok.subject)
		match = func(s Session) bool { return s.UserID == tok.subject }
	}
	if err != nil {
		return 0, nil, err
	}
	n, subjects, err := c.deleteMatching(ctx, ids, match)
	if err != nil {
		return n, subjects, err
	}
	if tok.subject != "" && !containsString(subjects, tok.subject) {
		subjects = append(subjects, tok.subject)
	}
	return n, subjects, nil
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func writeLogoutError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
}

// stringClaim returns claims[name] when it is a string, otherwise "".
func stringClaim(claims map[string]any, name string) string {
	s, _ := claims[name].(string)
	return s
}
