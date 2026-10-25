package oauth2

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	xoauth2 "golang.org/x/oauth2"
)

const (
	// maxMaxAge bounds the max_age login parameter (one year in seconds).
	maxMaxAge = 365 * 24 * 60 * 60
	// authTimeSkew tolerates a provider clock slightly behind ours when
	// checking auth_time against max_age.
	authTimeSkew = time.Minute
)

// allowedPrompts are the OpenID Connect Core prompt values accepted on
// GET /oauth/login.
var allowedPrompts = map[string]bool{"none": true, "login": true, "consent": true, "select_account": true}

// loginParams are the validated step-up parameters of a login request.
type loginParams struct {
	prompt    string
	maxAge    *int
	loginHint string
}

// parseLoginParams validates the optional prompt, max_age and login_hint
// query parameters. prompt is a space-separated subset of none, login, consent and
// select_account ("none" only alone); max_age is a decimal integer from 0 to
// one year in seconds.
func parseLoginParams(q url.Values) (loginParams, error) {
	var p loginParams
	if q.Has("prompt") {
		values := strings.Split(q.Get("prompt"), " ")
		seen := make(map[string]bool, len(values))
		for _, v := range values {
			if !allowedPrompts[v] || seen[v] {
				return loginParams{}, errors.New("oauth2: invalid prompt")
			}
			seen[v] = true
		}
		if seen["none"] && len(values) > 1 {
			return loginParams{}, errors.New("oauth2: invalid prompt")
		}
		p.prompt = strings.Join(values, " ")
	}
	if q.Has("max_age") {
		raw := q.Get("max_age")
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > maxMaxAge || strconv.Itoa(n) != raw {
			return loginParams{}, errors.New("oauth2: invalid max_age")
		}
		p.maxAge = &n
	}
	if q.Has("login_hint") {
		hint := q.Get("login_hint")
		if !validLoginHint(hint) {
			return loginParams{}, errors.New("oauth2: invalid login_hint")
		}
		p.loginHint = hint
	}
	return p, nil
}

// authCodeOptions returns the authorization request parameters for p.
func (p loginParams) authCodeOptions() []xoauth2.AuthCodeOption {
	var opts []xoauth2.AuthCodeOption
	if p.prompt != "" {
		opts = append(opts, xoauth2.SetAuthURLParam("prompt", p.prompt))
	}
	if p.maxAge != nil {
		opts = append(opts, xoauth2.SetAuthURLParam("max_age", strconv.Itoa(*p.maxAge)))
	}
	if p.loginHint != "" {
		opts = append(opts, xoauth2.SetAuthURLParam("login_hint", p.loginHint))
	}
	return opts
}

// satisfiesMaxAge reports whether authTime meets the max_age requested at
// requestedAt: the authentication may be at most maxAge seconds older than
// the login request (OpenID Connect Core 1.0 section 3.1.2.1).
func satisfiesMaxAge(authTime, requestedAt time.Time, maxAge int) bool {
	if authTime.IsZero() {
		return false
	}
	return !authTime.Before(requestedAt.Add(-time.Duration(maxAge)*time.Second - authTimeSkew))
}

// authenticationClaims extracts amr and auth_time from decoded ID-token
// claims. Malformed values are ignored.
func authenticationClaims(claims map[string]any) ([]string, time.Time) {
	var amr []string
	if list, ok := claims["amr"].([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok && s != "" {
				amr = append(amr, s)
			}
		}
	}
	var authTime time.Time
	// Bounded to plausible Unix seconds so a hostile value cannot overflow.
	if v, ok := claims["auth_time"].(float64); ok && v > 0 && v < 1e11 {
		authTime = time.Unix(int64(v), 0)
	}
	return amr, authTime
}

// maxLoginHint bounds login_hint to the longest email address.
const maxLoginHint = 254

// validLoginHint: a login name, an address or a provider's domain hint
// ("@example.com"), without spaces or control characters.
func validLoginHint(hint string) bool {
	if hint == "" || len(hint) > maxLoginHint || !utf8.ValidString(hint) {
		return false
	}
	return !strings.ContainsFunc(hint, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// stepUpHint names the signed-in person to the provider when a login
// re-authenticates them (prompt or max_age), so they confirm who they are
// instead of choosing again: their preferred_username claim.
func (c *Client) stepUpHint(r *http.Request, p loginParams) string {
	if p.loginHint != "" || (p.prompt == "" && p.maxAge == nil) {
		return p.loginHint
	}
	cookie, err := r.Cookie(c.cookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	sess, err := c.sessions.Get(r.Context(), cookie.Value)
	if err != nil {
		return ""
	}
	name, _ := sess.Claims["preferred_username"].(string)
	if !validLoginHint(name) {
		return ""
	}
	return name
}
