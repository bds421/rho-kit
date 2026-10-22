package oauth2

import (
	"cmp"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxPendingSignIns bounds the per-state cookies one browser holds, so many
// tabs signing in at once cannot grow the cookie header without limit.
const maxPendingSignIns = 5

// pendingRecoveryWindow keeps a pending sign-in known past stateTTL, so a
// callback that arrives too late still finds its return URL and restarts the
// sign-in instead of failing.
const pendingRecoveryWindow = time.Hour

func (c *Client) pendingLifetime() time.Duration {
	return c.stateTTL + pendingRecoveryWindow
}

func (c *Client) pendingCookiePrefix() string {
	return c.cookieName + "_state_"
}

// pendingCookieName keys the browser binding by the state value, so
// concurrent sign-ins in several tabs each keep their own cookie.
func (c *Client) pendingCookieName(state string) string {
	return c.pendingCookiePrefix() + stateBindingValue(state)[:16]
}

// stateBindingValue returns the cookie value for a login state token. It is a
// hash rather than the raw state so a leaked cookie alone does not give an
// attacker the full state parameter.
func stateBindingValue(state string) string {
	sum := sha256.Sum256([]byte(state))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (c *Client) pendingCookie(state string, now time.Time) *http.Cookie {
	lifetime := c.pendingLifetime()
	return &http.Cookie{
		Name:     c.pendingCookieName(state),
		Value:    strconv.FormatInt(now.UnixNano(), 10) + "." + stateBindingValue(state),
		Path:     "/oauth/",
		Domain:   c.cookieDomain,
		HttpOnly: true,
		Secure:   c.cookieSecure,
		// Lax so the cookie is sent on the top-level IdP redirect back to
		// /callback (Strict would drop it on that hop).
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(lifetime.Seconds()),
		Expires:  now.Add(lifetime),
	}
}

func (c *Client) expiredCookie(name, path string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     path,
		Domain:   c.cookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

// isBound reports whether the browser holds the binding cookie minted for
// state at /login. Server-side single-use storage alone only prevents replay;
// the browser binding is what stops login CSRF (session swap).
func (c *Client) isBound(r *http.Request, state string) bool {
	if state == "" {
		return false
	}
	cookie, err := r.Cookie(c.pendingCookieName(state))
	if err != nil {
		return false
	}
	_, binding, ok := strings.Cut(cookie.Value, ".")
	return ok && subtle.ConstantTimeCompare([]byte(binding), []byte(stateBindingValue(state))) == 1
}

func (c *Client) clearPending(w http.ResponseWriter, state string) {
	if state != "" {
		http.SetCookie(w, c.expiredCookie(c.pendingCookieName(state), "/oauth/"))
	}
}

// startPending binds a new state to the browser, dropping the oldest pending
// sign-ins beyond maxPendingSignIns and the single cookie older releases set.
func (c *Client) startPending(w http.ResponseWriter, r *http.Request, state string, now time.Time) {
	type pending struct {
		name    string
		started int64
	}
	var open []pending
	for _, cookie := range r.Cookies() {
		if cookie.Name == c.cookieName+"_state" {
			http.SetCookie(w, c.expiredCookie(cookie.Name, "/"))
			continue
		}
		if !strings.HasPrefix(cookie.Name, c.pendingCookiePrefix()) {
			continue
		}
		started, _, _ := strings.Cut(cookie.Value, ".")
		unix, _ := strconv.ParseInt(started, 10, 64)
		open = append(open, pending{name: cookie.Name, started: unix})
	}
	slices.SortFunc(open, func(a, b pending) int { return cmp.Compare(a.started, b.started) })
	for len(open) >= maxPendingSignIns {
		http.SetCookie(w, c.expiredCookie(open[0].name, "/oauth/"))
		open = open[1:]
	}
	http.SetCookie(w, c.pendingCookie(state, now))
}
