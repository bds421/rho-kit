package oidc

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/bds421/rho-kit/core/v2/contextutil"
)

// ErrNoSessionClaims is returned by [DecodeSessionClaims] when the request
// has no authenticated browser session.
var ErrNoSessionClaims = errors.New("app/oidc: no session claims in context")

var sessionClaimsKey = contextutil.NewKey[map[string]any]("app.oidc.session_claims")

// SessionClaims returns a deep copy of the verified ID-token claims of the
// request's browser session, as decoded from JSON (objects are
// map[string]any, arrays []any, numbers float64). It complements the
// canonical identity.Principal, which carries only mapped string values, for
// provider claims with object or array shape. ok is false when the request
// has no session. The module middleware sets it for every route except the
// fixed /oauth/* routes.
//
// The claims are the ID token's, refreshed together with it when token
// refresh is enabled; authorization decisions should still prefer the
// mapped Principal or a dedicated policy over ad hoc claim reads.
func SessionClaims(ctx context.Context) (map[string]any, bool) {
	claims, ok := sessionClaimsKey.Get(ctx)
	if !ok {
		return nil, false
	}
	return cloneClaims(claims), true
}

// DecodeSessionClaims decodes the session claims into dst with
// encoding/json semantics, so object and array claims map onto typed
// fields:
//
//	var c struct {
//	    Roles map[string]map[string]string `json:"urn:example:roles"`
//	}
//	err := oidc.DecodeSessionClaims(r.Context(), &c)
//
// Returns [ErrNoSessionClaims] without a session.
func DecodeSessionClaims(ctx context.Context, dst any) error {
	claims, ok := sessionClaimsKey.Get(ctx)
	if !ok {
		return ErrNoSessionClaims
	}
	b, err := json.Marshal(claims)
	if err != nil {
		return errors.New("app/oidc: encode session claims")
	}
	return json.Unmarshal(b, dst)
}

func withSessionClaims(ctx context.Context, claims map[string]any) context.Context {
	if claims == nil {
		claims = map[string]any{}
	}
	return sessionClaimsKey.Set(ctx, cloneClaims(claims))
}

func cloneClaims(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneClaimValue(v)
	}
	return out
}

func cloneClaimValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneClaims(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = cloneClaimValue(item)
		}
		return out
	case []string:
		return append([]string(nil), x...)
	default:
		return v
	}
}
