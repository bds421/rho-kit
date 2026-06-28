package interceptor

import (
	"context"

	"google.golang.org/grpc/metadata"
)

// Outgoing metadata keys for propagating verified identity across gRPC hops.
// Mirror the HTTP X-User-Id convention; values are stamped only from auth
// middleware context — never copy unverified inbound metadata through.
const (
	MetadataSubjectKey    = "x-subject-id"
	MetadataActorKey      = "x-actor-id"
	MetadataActorKindKey  = "x-actor-kind"
	MetadataLegacyUserKey = xUserIDMetadataKey
)

// AppendOutgoingIdentity copies Subject, Actor, and ActorKind from ctx into
// outgoing gRPC metadata when not already set. Use from client interceptors
// so downstream services can admit internal calls without re-parsing JWTs.
func AppendOutgoingIdentity(ctx context.Context) context.Context {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		md = metadata.MD{}
	} else {
		md = md.Copy()
	}
	if subj := Subject(ctx); subj != "" && len(md.Get(MetadataSubjectKey)) == 0 {
		md.Set(MetadataSubjectKey, subj)
		md.Set(MetadataLegacyUserKey, subj)
	}
	if actor := Actor(ctx); actor != "" && len(md.Get(MetadataActorKey)) == 0 {
		md.Set(MetadataActorKey, actor)
	}
	if kind := ActorKindFromContext(ctx); kind != "" && len(md.Get(MetadataActorKindKey)) == 0 {
		md.Set(MetadataActorKindKey, string(kind))
	}
	return metadata.NewOutgoingContext(ctx, md)
}