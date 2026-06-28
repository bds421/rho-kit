package interceptor

import (
	"context"
	"slices"

	"github.com/bds421/rho-kit/core/v2/contextutil"
)

// ActorKind classifies who performed the request. Values match
// httpx/middleware/auth.ActorKind so audit and policy strings stay portable.
type ActorKind string

const (
	ActorUser        ActorKind = "user"
	ActorAPIKey      ActorKind = "api_key"
	ActorOAuthClient ActorKind = "oauth_client"
	ActorService     ActorKind = "service"
)

type grpcSubject string
type grpcActor string

var (
	subjectKey    = contextutil.NewKey[grpcSubject]("grpcx.auth.subject")
	actorKey      = contextutil.NewKey[grpcActor]("grpcx.auth.actor")
	actorKindKey  = contextutil.NewKey[ActorKind]("grpcx.auth.actor_kind")
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
	switch kind {
	case ActorAPIKey, ActorOAuthClient, ActorService:
		return true
	default:
		return false
	}
}

// IsMachine reports whether ctx carries a non-human actor kind.
func IsMachine(ctx context.Context) bool {
	return IsMachineKind(ActorKindFromContext(ctx))
}