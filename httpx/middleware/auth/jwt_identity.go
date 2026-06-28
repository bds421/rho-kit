package auth

import (
	"github.com/bds421/rho-kit/security/v2/identity"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

type jwtIdentityConfig struct {
	mapping identity.JWTActorMapping
}

// JWTOption configures how verified JWT claims map to [Identity] for [JWT],
// [NewJWTAuthenticator], and the JWT branch of [RequireS2SAuth].
type JWTOption func(*jwtIdentityConfig)

// WithJWTActorFromClaim sets Actor from the named JWT claim when non-empty.
// ActorKind stays [ActorUser] unless [WithJWTServiceActorFromClaim] matches.
func WithJWTActorFromClaim(claim string) JWTOption {
	if claim == "" {
		panic("middleware/auth: WithJWTActorFromClaim requires a non-empty claim name")
	}
	return func(c *jwtIdentityConfig) { c.mapping.ActorClaim = claim }
}

// WithJWTServiceActorFromClaim stamps [ActorService] when the named claim is
// non-empty. Actor becomes the claim value; Subject remains normalized sub.
// Common values: "client_id", "azp".
func WithJWTServiceActorFromClaim(claim string) JWTOption {
	if claim == "" {
		panic("middleware/auth: WithJWTServiceActorFromClaim requires a non-empty claim name")
	}
	return func(c *jwtIdentityConfig) { c.mapping.ServiceActorClaim = claim }
}

// WithS2SJWTIdentity applies JWT identity-mapping options to the JWT branch
// of [RequireS2SAuth] and [RequireS2SAuthWithIdentity].
func WithS2SJWTIdentity(opts ...JWTOption) MTLSIdentityOption {
	cfg := buildJWTIdentityConfig(opts...)
	return func(m *mtlsIdentityConfig) { m.jwt = cfg }
}

func buildJWTIdentityConfig(opts ...JWTOption) jwtIdentityConfig {
	cfg := jwtIdentityConfig{}
	for _, o := range opts {
		if o == nil {
			panic("middleware/auth: JWTOption must not be nil")
		}
		o(&cfg)
	}
	return cfg
}

func identityFromJWTClaims(claims *jwtutil.Claims, cfg jwtIdentityConfig) (Identity, bool) {
	subj, actor, kind, ok := identity.ApplyJWTActor(claims.Subject, claims.StringClaim, cfg.mapping)
	if !ok {
		return Identity{}, false
	}
	return Identity{
		Subject:     subj,
		Actor:       actor,
		ActorKind:   kind,
		Permissions: claims.Permissions,
		Scopes:      claims.Scopes,
	}.Normalize(), true
}