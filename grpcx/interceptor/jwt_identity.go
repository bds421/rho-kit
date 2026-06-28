package interceptor

import (
	"github.com/bds421/rho-kit/security/v2/identity"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

type jwtIdentityConfig struct {
	mapping identity.JWTActorMapping
}

// JWTOption configures how verified JWT claims map to gRPC auth context for
// [AuthUnary], [AuthStream], and the JWT branch of [MTLSAuthUnary].
type JWTOption func(*jwtIdentityConfig)

// WithJWTActorFromClaim sets Actor from the named JWT claim when non-empty.
func WithJWTActorFromClaim(claim string) JWTOption {
	if claim == "" {
		panic("grpcx/interceptor: WithJWTActorFromClaim requires a non-empty claim name")
	}
	return func(c *jwtIdentityConfig) { c.mapping.ActorClaim = claim }
}

// WithJWTServiceActorFromClaim stamps [ActorService] when the named claim is
// non-empty. Common values: "client_id", "azp".
func WithJWTServiceActorFromClaim(claim string) JWTOption {
	if claim == "" {
		panic("grpcx/interceptor: WithJWTServiceActorFromClaim requires a non-empty claim name")
	}
	return func(c *jwtIdentityConfig) { c.mapping.ServiceActorClaim = claim }
}

// WithMTLSJWTIdentity applies JWT identity-mapping options to the JWT branch
// of [MTLSAuthUnary] and [MTLSAuthStream].
func WithMTLSJWTIdentity(opts ...JWTOption) MTLSIdentityOption {
	cfg := buildJWTIdentityConfig(opts...)
	return func(m *mtlsIdentityConfig) { m.jwt = cfg }
}

func buildJWTIdentityConfig(opts ...JWTOption) jwtIdentityConfig {
	cfg := jwtIdentityConfig{}
	for _, o := range opts {
		if o == nil {
			panic("grpcx/interceptor: JWTOption must not be nil")
		}
		o(&cfg)
	}
	return cfg
}

func subjectActorFromJWTClaims(claims *jwtutil.Claims, cfg jwtIdentityConfig) (subject, actor string, kind ActorKind, ok bool) {
	subj, act, k, ok := identity.ApplyJWTActor(claims.Subject, claims.StringClaim, cfg.mapping)
	return subj, act, k, ok
}

// AsAuthOption adapts a [JWTOption] for [AuthUnary] and [AuthStream].
func AsAuthOption(opt JWTOption) AuthOption {
	return func(c *authConfig) { opt(&c.jwt) }
}