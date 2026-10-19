package oidc

import (
	"context"
	"slices"
	"time"

	"github.com/bds421/rho-kit/core/v2/contextutil"
)

type sessionAuthentication struct {
	amr      []string
	authTime time.Time
}

var sessionAuthenticationKey = contextutil.NewKey[sessionAuthentication]("app.oidc.session_authentication")

// SessionAuthentication returns how the user of the request's browser
// session authenticated: the ID token's amr (authentication methods) and
// auth_time. ok is false when the request has no session; amr is nil and
// authTime zero when the provider did not issue the claims.
//
// Use it for step-up checks: when an operation needs a recent or stronger
// authentication, check authTime and amr and, if insufficient, send the
// browser to /oauth/login?prompt=login&max_age=0 (the callback then rejects
// an auth_time older than the login request):
//
//	amr, authTime, ok := oidc.SessionAuthentication(r.Context())
//	if !ok || time.Since(authTime) > 5*time.Minute || !slices.Contains(amr, "otp") {
//	    // answer 401 with a step-up hint
//	}
func SessionAuthentication(ctx context.Context) (amr []string, authTime time.Time, ok bool) {
	a, ok := sessionAuthenticationKey.Get(ctx)
	if !ok {
		return nil, time.Time{}, false
	}
	return slices.Clone(a.amr), a.authTime, true
}

func withSessionAuthentication(ctx context.Context, amr []string, authTime time.Time) context.Context {
	return sessionAuthenticationKey.Set(ctx, sessionAuthentication{amr: slices.Clone(amr), authTime: authTime})
}
