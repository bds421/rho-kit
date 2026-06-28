package interceptor

import (
	"context"
	"slices"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/security/v2/identity"
)

// ActorKind classifies who performed the request. Alias of [identity.ActorKind].
type ActorKind = identity.ActorKind

const (
	ActorUser        = identity.ActorUser
	ActorAPIKey      = identity.ActorAPIKey
	ActorOAuthClient = identity.ActorOAuthClient
	ActorService     = identity.ActorService
)

type grpcSubject string
type grpcActor string

var (
	subjectKey   = contextutil.NewKey[grpcSubject]("grpcx.auth.subject")
	actorKey     = contextutil.NewKey[grpcActor]("grpcx.auth.actor")
	actorKindKey = contextutil.NewKey[ActorKind]("grpcx.auth.actor_kind")
)

// stampIdentity writes subject/actor fields onto ctx. Mirrors the HTTP
// httpx/middleware/auth stamp contract; permissions and scopes are optional.
func stampIdentity(ctx context.Context, subject, actor string, kind ActorKind, perms []string, scopes string, trusted bool) context.Context {
	if subject != "" {
		ctx = subjectKey.Set(ctx, grpcSubject(subject))
		ctx = userIDKey.Set(ctx, grpcUserID(subject))
	}
	if actor != "" {
		ctx = actorKey.Set(ctx, grpcActor(actor))
	}
	if kind != "" {
		ctx = actorKindKey.Set(ctx, kind)
	}
	perms = slices.Clone(perms)
	ctx = permissionsKey.Set(ctx, grpcPermissions(perms))
	ctx = scopesKey.Set(ctx, grpcScopes(scopes))
	if trusted {
		ctx = trustedS2SKey.Set(ctx, grpcTrustedS2SMarker{})
	}
	return ctx
}

// Subject extracts the visibility subject UUID from context. Falls back to
// [UserID] when only the legacy user_id key was stamped.
func Subject(ctx context.Context) string {
	v, ok := subjectKey.Get(ctx)
	if ok && v != "" {
		return string(v)
	}
	return UserID(ctx)
}

// Actor extracts the attribution id (service identity, key id, or user UUID).
func Actor(ctx context.Context) string {
	v, _ := actorKey.Get(ctx)
	return string(v)
}

// ActorKindFromContext returns the actor classification stamped by auth middleware.
func ActorKindFromContext(ctx context.Context) ActorKind {
	v, _ := actorKindKey.Get(ctx)
	return v
}

// IsMachineKind reports whether kind represents a non-human actor.
func IsMachineKind(kind ActorKind) bool {
	return identity.IsMachineKind(kind)
}

// IsMachine reports whether ctx carries a non-human actor kind.
func IsMachine(ctx context.Context) bool {
	return identity.IsMachineKind(ActorKindFromContext(ctx))
}

// FormatActor returns the conventional actionlog/audit actor string for ctx.
func FormatActor(ctx context.Context) string {
	return identity.Format(identity.Ref{
		Subject: Subject(ctx),
		Actor:   Actor(ctx),
		Kind:    ActorKindFromContext(ctx),
	})
}