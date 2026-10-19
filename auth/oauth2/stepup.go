package oauth2

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	prompt string
	maxAge *int
}

// parseLoginParams validates the optional prompt and max_age query
// parameters. prompt is a space-separated subset of none, login, consent and
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
