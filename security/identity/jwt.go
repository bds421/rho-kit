package identity

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// maxActorClaimLen caps claim-derived actor strings written into audit
// logs via [Format]. Keeps a misbehaving issuer from bloating action logs.
const maxActorClaimLen = 128

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
//
// Actor values taken from ServiceActorClaim/ActorClaim are sanitized: control
// characters and whitespace are rejected, and length is capped at
// maxActorClaimLen. Malformed claim values fall back to the UUID subject so a
// federated issuer cannot inject newlines into audit actor strings.
func ApplyJWTActor(subject string, claim func(string) (string, bool), mapping JWTActorMapping) (subj, actor string, kind ActorKind, ok bool) {
	subj, ok = jwtutil.NormalizeSubjectID(subject)
	if !ok {
		return "", "", "", false
	}
	actor = subj
	kind = ActorUser
	if mapping.ServiceActorClaim != "" && claim != nil {
		if v, ok := claim(mapping.ServiceActorClaim); ok {
			if sanitized, good := sanitizeActorClaim(v); good {
				kind = ActorService
				actor = sanitized
			}
		}
	}
	if mapping.ActorClaim != "" && claim != nil {
		if v, ok := claim(mapping.ActorClaim); ok {
			if sanitized, good := sanitizeActorClaim(v); good {
				actor = sanitized
			}
		}
	}
	return subj, actor, kind, true
}

// sanitizeActorClaim accepts printable, non-whitespace actor identifiers
// suitable for audit logs. Rejects control characters, spaces, and overlong
// values; trims surrounding space only when the result remains non-empty.
func sanitizeActorClaim(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > maxActorClaimLen || !utf8.ValidString(v) {
		return "", false
	}
	for _, r := range v {
		if r == utf8.RuneError {
			return "", false
		}
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", false
		}
	}
	return v, true
}
