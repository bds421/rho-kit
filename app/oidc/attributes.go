package oidc

import (
	"context"
	"maps"

	"github.com/bds421/rho-kit/core/v2/contextutil"
)

var sessionAttributesKey = contextutil.NewKey[map[string]string]("app.oidc.session_attributes")

// SessionAttributes returns a copy of the attributes the application attached
// to the request's browser session at sign-in (auth/oauth2.WithSignInHook),
// for example the person's identifier in the application's own directory.
// ok is false when the request has no session.
func SessionAttributes(ctx context.Context) (map[string]string, bool) {
	attrs, ok := sessionAttributesKey.Get(ctx)
	if !ok {
		return nil, false
	}
	return maps.Clone(attrs), true
}

func withSessionAttributes(ctx context.Context, attrs map[string]string) context.Context {
	if attrs == nil {
		attrs = map[string]string{}
	}
	return sessionAttributesKey.Set(ctx, maps.Clone(attrs))
}
