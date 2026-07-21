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
	MetadataSubjectKey     = "x-subject-id"
	MetadataActorKey       = "x-actor-id"
	MetadataActorKindKey   = "x-actor-kind"
	MetadataLegacyUserKey  = xUserIDMetadataKey
	MetadataPermissionsKey = "x-permissions"
	MetadataScopesKey      = "x-scopes"
)

// maxOutgoingEntitlements bounds how many permission/scope tokens a single
// hop may stamp so a hostile JWT claim cannot inflate per-RPC headers.
const maxOutgoingEntitlements = 64

// AppendOutgoingIdentity copies Subject, Actor, ActorKind, permissions, and
// scopes from ctx into outgoing gRPC metadata when not already set. Use from
// client interceptors so downstream services can admit internal calls without
// re-parsing JWTs and still enforce user entitlements on trusted-S2S hops.
//
// Identity is stamped for every outbound call that carries an authenticated
// ctx — there is no per-host allowlist. Do not attach the default propagation
// interceptors to clients that dial untrusted or third-party endpoints, or
// opt out with client.WithoutIdentityPropagation, so internal UUIDs and
// entitlement claims are not disclosed off-trust-boundary.
//
// Actor/kind values that are not printable ASCII metadata-safe (control/space/
// non-UTF-8/comma, or overlong) are skipped rather than failing every
// downstream RPC at header encoding time. Permission and scope tokens are
// validated with the same rules and emitted as multi-value metadata entries.
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
	if perms := UserPermissions(ctx); len(perms) > 0 && len(md.Get(MetadataPermissionsKey)) == 0 {
		if safe := filterSafeEntitlements(perms); len(safe) > 0 {
			md.Set(MetadataPermissionsKey, safe...)
		}
	}
	if scopes := UserScopes(ctx); scopes != "" && len(md.Get(MetadataScopesKey)) == 0 {
		if safe := filterSafeEntitlements(strings.Fields(scopes)); len(safe) > 0 {
			md.Set(MetadataScopesKey, safe...)
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

func filterSafeEntitlements(vals []string) []string {
	if len(vals) == 0 {
		return nil
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if !isSafeOutgoingIdentity(v) {
			continue
		}
		out = append(out, v)
		if len(out) >= maxOutgoingEntitlements {
			break
		}
	}
	return out
}

// AdoptIncomingIdentity copies x-subject-id / x-actor-id / x-actor-kind /
// x-permissions / x-scopes from inbound metadata into the request context when
// the call is already marked trusted-S2S (e.g. after mTLS S2S admission).
// Values are re-validated with the same safety rules as the outgoing path.
// Untrusted calls are unchanged so clients cannot self-assert actor or
// entitlement metadata.
//
// Wire this after MTLS/JWT auth interceptors when downstream services should
// honour the headers [AppendOutgoingIdentity] emits. Existing subject/actor/
// kind/permissions/scopes already on ctx are preserved; only empty slots are
// filled. [MTLSAuthUnary] / [MTLSAuthStream] invoke this automatically on the
// mTLS branch so entitlement propagation works without a separate interceptor.
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
	if existing, ok := permissionsKey.Get(ctx); !ok || len(existing) == 0 {
		if vals := md.Get(MetadataPermissionsKey); len(vals) > 0 {
			if safe := filterSafeEntitlements(vals); len(safe) > 0 {
				ctx = permissionsKey.Set(ctx, grpcPermissions(safe))
			}
		}
	}
	if existing, ok := scopesKey.Get(ctx); !ok || existing == "" {
		if vals := md.Get(MetadataScopesKey); len(vals) > 0 {
			if safe := filterSafeEntitlements(vals); len(safe) > 0 {
				ctx = scopesKey.Set(ctx, grpcScopes(strings.Join(safe, " ")))
			}
		}
	}
	return ctx
}
