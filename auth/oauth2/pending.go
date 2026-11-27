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

// binding ties a login state to the browser that started it: one cookie per
// pending sign-in, keyed by a hash of the state. [Client] and [Flow] share it.
type binding struct {
	prefix   string
	path     string
	domain   string
	secure   bool
	lifetime time.Duration
	// legacy is the single cookie name older releases set; it is cleared.
	legacy string
}

func (c *Client) pendingLifetime() time.Duration {
	return c.stateTTL + pendingRecoveryWindow
}

func (c *Client) binding() binding {
	return binding{
		prefix:   c.cookieName + "_state_",
		path:     c.flowCookiePath(),
		domain:   c.cookieDomain,
		secure:   c.cookieSecure,
		lifetime: c.pendingLifetime(),
		legacy:   c.cookieName + "_state",
	}
}

// cookieName keys the browser binding by the state value, so concurrent
// sign-ins in several tabs each keep their own cookie.
func (b binding) cookieName(state string) string {
	return b.prefix + stateBindingValue(state)[:16]
}

// stateBindingValue returns the cookie value for a login state token. It is a
// hash rather than the raw state so a leaked cookie alone does not give an
// attacker the full state parameter.
func stateBindingValue(state string) string {
	sum := sha256.Sum256([]byte(state))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (b binding) cookie(state string, now time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     b.cookieName(state),
		Value:    strconv.FormatInt(now.UnixNano(), 10) + "." + stateBindingValue(state),
		Path:     b.path,
		Domain:   b.domain,
		HttpOnly: true,
		Secure:   b.secure,
		// Lax so the cookie is sent on the top-level IdP redirect back to
		// the callback (Strict would drop it on that hop).
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(b.lifetime.Seconds()),
		Expires:  now.Add(b.lifetime),
	}
}

func (b binding) expired(name, path string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     path,
		Domain:   b.domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   b.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (c *Client) expiredCookie(name, path string) *http.Cookie {
	return c.binding().expired(name, path)
}

// bound reports whether the browser holds the binding cookie minted for
// state at login. Server-side single-use storage alone only prevents replay;
// the browser binding is what stops login CSRF (session swap).
func (b binding) bound(r *http.Request, state string) bool {
	if state == "" {
		return false
	}
	cookie, err := r.Cookie(b.cookieName(state))
	if err != nil {
		return false
	}
	_, value, ok := strings.Cut(cookie.Value, ".")
	return ok && subtle.ConstantTimeCompare([]byte(value), []byte(stateBindingValue(state))) == 1
}

func (b binding) clear(w http.ResponseWriter, state string) {
	if state != "" {
		http.SetCookie(w, b.expired(b.cookieName(state), b.path))
	}
}

// start binds a new state to the browser, dropping the oldest pending
// sign-ins beyond maxPendingSignIns and the single cookie older releases set.
func (b binding) start(w http.ResponseWriter, r *http.Request, state string, now time.Time) {
	type pending struct {
		name    string
		started int64
	}
	var open []pending
	for _, cookie := range r.Cookies() {
		if b.legacy != "" && cookie.Name == b.legacy {
			http.SetCookie(w, b.expired(cookie.Name, "/"))
			continue
		}
		if !strings.HasPrefix(cookie.Name, b.prefix) {
			continue
		}
		started, _, _ := strings.Cut(cookie.Value, ".")
		unix, _ := strconv.ParseInt(started, 10, 64)
		open = append(open, pending{name: cookie.Name, started: unix})
	}
	slices.SortFunc(open, func(a, b pending) int { return cmp.Compare(a.started, b.started) })
	for len(open) >= maxPendingSignIns {
		http.SetCookie(w, b.expired(open[0].name, b.path))
		open = open[1:]
	}
	http.SetCookie(w, b.cookie(state, now))
}

func (c *Client) isBound(r *http.Request, state string) bool { return c.binding().bound(r, state) }

func (c *Client) clearPending(w http.ResponseWriter, state string) { c.binding().clear(w, state) }

func (c *Client) startPending(w http.ResponseWriter, r *http.Request, state string, now time.Time) {
	c.binding().start(w, r, state, now)
}
