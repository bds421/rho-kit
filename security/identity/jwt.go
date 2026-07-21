package identity

import (
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// JWTActorMapping configures how verified JWT claims map to Subject, Actor,
// and ActorKind. Zero value maps sub to both Subject and Actor as [ActorUser].
type JWTActorMapping struct {
	// ActorClaim, when set, overrides Actor when the claim is non-empty.
	ActorClaim string
	// ServiceActorClaim, when the named claim is non-empty, stamps
	// [ActorService] and uses the claim value as Actor. Subject remains sub.
	ServiceActorClaim string
}

// ApplyJWTActor normalizes subject and derives actor/kind using mapping.
// claim reads string JWT claims (typically [jwtutil.Claims.StringClaim]).
func ApplyJWTActor(subject string, claim func(string) (string, bool), mapping JWTActorMapping) (subj, actor string, kind ActorKind, ok bool) {
	subj, ok = jwtutil.NormalizeSubjectID(subject)
	if !ok {
		return "", "", "", false
	}
	actor = subj
	kind = ActorUser
	if mapping.ServiceActorClaim != "" {
		if v, ok := claim(mapping.ServiceActorClaim); ok {
			kind = ActorService
			actor = v
		}
	}
	if mapping.ActorClaim != "" {
		if v, ok := claim(mapping.ActorClaim); ok {
			actor = v
		}
	}
	return subj, actor, kind, true
}
