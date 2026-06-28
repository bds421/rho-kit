package auth

import (
	"slices"
	"strings"

	"github.com/bds421/rho-kit/security/v2/apikey"
	"github.com/bds421/rho-kit/security/v2/identity"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// ActorKind classifies who performed the request. Alias of [identity.ActorKind].
type ActorKind = identity.ActorKind

const (
	ActorUser        = identity.ActorUser
	ActorAPIKey      = identity.ActorAPIKey
	ActorOAuthClient = identity.ActorOAuthClient
	ActorService     = identity.ActorService
	ActorAnonymous   = identity.ActorAnonymous
)

// IsMachineKind reports whether kind represents a non-human actor.
func IsMachineKind(kind ActorKind) bool {
	return identity.IsMachineKind(kind)
}

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
func FormatActor(id Identity) string {
	id = id.Normalize()
	return identity.Format(identity.Ref{
		Subject: id.Subject,
		Actor:   id.Actor,
		Kind:    id.ActorKind,
	})
}