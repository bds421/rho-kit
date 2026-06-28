package auth

import (
	"slices"
	"strings"

	"github.com/bds421/rho-kit/security/v2/apikey"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// ActorKind classifies who performed the request for audit, rate limits, and
// policy branches. Apps may prefix [Identity.Actor] strings at their boundary;
// kind drives kit middleware behavior.
type ActorKind string

const (
	// ActorUser is a human or user-shaped session/JWT subject.
	ActorUser ActorKind = "user"
	// ActorAPIKey is a scoped or header API key credential.
	ActorAPIKey ActorKind = "api_key"
	// ActorOAuthClient is an OAuth client-credentials access token.
	ActorOAuthClient ActorKind = "oauth_client"
	// ActorService is a trusted service identity (JWT S2S, mTLS, internal).
	ActorService ActorKind = "service"
	// ActorAnonymous is the zero value; [Identity.Normalize] treats it as user
	// when Subject is set without an explicit kind.
	ActorAnonymous ActorKind = ""
)

// Normalize fills Subject/Actor/ActorKind from legacy [Identity.UserID] and
// syncs [Identity.Scopes] with [Identity.ScopeList]. Call before stamping or
// validating an identity returned by custom authenticators.
func (id Identity) Normalize() Identity {
	if id.Subject != "" {
		if norm, ok := jwtutil.NormalizeSubjectID(id.Subject); ok {
			id.Subject = norm
		}
	}
	if id.Subject == "" && id.UserID != "" {
		if norm, ok := jwtutil.NormalizeSubjectID(id.UserID); ok {
			id.Subject = norm
			id.UserID = norm
		} else {
			id.Subject = id.UserID
		}
	}
	if id.UserID == "" && id.Subject != "" {
		id.UserID = id.Subject
	}
	if id.Actor == "" && id.Subject != "" && (id.ActorKind == "" || id.ActorKind == ActorUser) {
		id.Actor = id.Subject
		id.ActorKind = ActorUser
	}
	if len(id.ScopeList) == 0 && id.Scopes != "" {
		id.ScopeList = strings.Fields(id.Scopes)
	}
	if id.Scopes == "" && len(id.ScopeList) > 0 {
		id.Scopes = strings.Join(id.ScopeList, " ")
	}
	return id
}

// IsMachineKind reports whether kind represents a non-human actor.
func IsMachineKind(kind ActorKind) bool {
	switch kind {
	case ActorAPIKey, ActorOAuthClient, ActorService:
		return true
	default:
		return false
	}
}

// identityValid reports whether id may be stamped after normalization.
func identityValid(id Identity) bool {
	id = id.Normalize()
	if id.Subject != "" {
		if _, ok := jwtutil.NormalizeSubjectID(id.Subject); !ok {
			return false
		}
	}
	switch id.ActorKind {
	case ActorAPIKey, ActorOAuthClient:
		if id.Actor == "" {
			return false
		}
		if id.Subject == "" && id.Tenant == "" {
			return false
		}
		return true
	case ActorService:
		return id.Actor != "" || id.Subject != ""
	case ActorUser, ActorAnonymous:
		return id.Subject != ""
	default:
		return id.Subject != ""
	}
}

// IdentityFromScopedKey maps [apikey.Principal] to [Identity] without
// collapsing the key id into Subject. Scope tokens are written to ScopeList,
// Scopes, and Permissions so both [RequireScope] and [RequirePermission] work.
func IdentityFromScopedKey(p apikey.Principal) Identity {
	scopes := slices.Clone(p.Scopes)
	id := Identity{
		Subject:     p.UserID,
		Actor:       p.KeyID,
		ActorKind:   actorKindFromScoped(p.Kind),
		Tenant:      p.Tenant,
		Role:        p.Role,
		ScopeList:   scopes,
		Permissions: slices.Clone(scopes),
	}
	return id.Normalize()
}

func actorKindFromScoped(kind apikey.ScopedKind) ActorKind {
	switch kind {
	case apikey.ScopedKindOAuthClient:
		return ActorOAuthClient
	default:
		return ActorAPIKey
	}
}

// FormatActor returns the conventional actionlog/audit actor string for id.
// Convention: "<actor_kind>:<actor_id>" (user actors use the UUID subject).
func FormatActor(id Identity) string {
	id = id.Normalize()
	switch id.ActorKind {
	case ActorUser, ActorAnonymous:
		if id.Subject != "" {
			return "user:" + id.Subject
		}
		return "user:" + id.Actor
	case ActorAPIKey, ActorOAuthClient, ActorService:
		if id.Actor == "" {
			return string(id.ActorKind) + ":"
		}
		return string(id.ActorKind) + ":" + id.Actor
	default:
		if id.Actor != "" {
			return string(id.ActorKind) + ":" + id.Actor
		}
		return "user:" + id.Subject
	}
}