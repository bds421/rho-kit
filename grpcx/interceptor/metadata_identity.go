package interceptor

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

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
//
// Identity is stamped for every outbound call that carries an authenticated
// ctx — there is no per-host allowlist. Do not attach the default propagation
// interceptors to clients that dial untrusted or third-party endpoints, or
// opt out with client.WithoutIdentityPropagation, so internal UUIDs are not
// disclosed off-trust-boundary.
//
// Actor values that are not printable ASCII metadata-safe (control/space/
// non-UTF-8/comma, or overlong) are skipped rather than failing every
// downstream RPC at header encoding time.
func AppendOutgoingIdentity(ctx context.Context) context.Context {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		md = metadata.MD{}
	} else {
		md = md.Copy()
	}
	if subj := Subject(ctx); subj != "" {
		// Never overwrite a pre-existing x-user-id / x-subject-id —
		// each key is guarded independently so a caller-set legacy
		// user id is preserved even when subject is empty-or-absent.
		if len(md.Get(MetadataSubjectKey)) == 0 {
			md.Set(MetadataSubjectKey, subj)
		}
		if len(md.Get(MetadataLegacyUserKey)) == 0 {
			md.Set(MetadataLegacyUserKey, subj)
		}
	}
	if actor := Actor(ctx); actor != "" && len(md.Get(MetadataActorKey)) == 0 {
		if isSafeOutgoingIdentity(actor) {
			md.Set(MetadataActorKey, actor)
		}
	}
	if kind := ActorKindFromContext(ctx); kind != "" && len(md.Get(MetadataActorKindKey)) == 0 {
		if isSafeOutgoingIdentity(string(kind)) {
			md.Set(MetadataActorKindKey, string(kind))
		}
	}
	return metadata.NewOutgoingContext(ctx, md)
}

// maxOutgoingIdentityBytes bounds actor/kind metadata values so a hostile
// JWT claim cannot inflate per-RPC headers.
const maxOutgoingIdentityBytes = 256

// isSafeOutgoingIdentity reports whether v is safe to place in non-bin
// gRPC metadata (printable, no spaces/commas/controls, valid UTF-8,
// bounded length). Mirrors singletonMetadataIdentity inbound rules.
func isSafeOutgoingIdentity(v string) bool {
	if v == "" || len(v) > maxOutgoingIdentityBytes {
		return false
	}
	if strings.TrimSpace(v) != v {
		return false
	}
	if !utf8.ValidString(v) || strings.Contains(v, ",") {
		return false
	}
	for _, r := range v {
		if r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}
// AdoptIncomingIdentity copies x-subject-id / x-actor-id / x-actor-kind from
// inbound metadata into the request context when the call is already marked
// trusted-S2S (e.g. after mTLS S2S admission). Values are re-validated with
// the same safety rules as the outgoing path. Untrusted calls are unchanged
// so clients cannot self-assert actor metadata.
//
// Wire this after MTLS/JWT auth interceptors when downstream services should
// honour the actor/kind headers [AppendOutgoingIdentity] emits. Existing
// subject/actor/kind already on ctx are preserved; only empty slots are filled.
func AdoptIncomingIdentity(ctx context.Context) context.Context {
	if !IsTrustedS2S(ctx) {
		return ctx
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	if Subject(ctx) == "" {
		if vals := md.Get(MetadataSubjectKey); len(vals) == 1 && isSafeOutgoingIdentity(vals[0]) {
			ctx = subjectKey.Set(ctx, grpcSubject(vals[0]))
			ctx = userIDKey.Set(ctx, grpcUserID(vals[0]))
		} else if vals := md.Get(MetadataLegacyUserKey); len(vals) == 1 && isSafeOutgoingIdentity(vals[0]) {
			ctx = subjectKey.Set(ctx, grpcSubject(vals[0]))
			ctx = userIDKey.Set(ctx, grpcUserID(vals[0]))
		}
	}
	if Actor(ctx) == "" {
		if vals := md.Get(MetadataActorKey); len(vals) == 1 && isSafeOutgoingIdentity(vals[0]) {
			ctx = actorKey.Set(ctx, grpcActor(vals[0]))
		}
	}
	if ActorKindFromContext(ctx) == "" {
		if vals := md.Get(MetadataActorKindKey); len(vals) == 1 && isSafeOutgoingIdentity(vals[0]) {
			ctx = actorKindKey.Set(ctx, ActorKind(vals[0]))
		}
	}
	return ctx
}
