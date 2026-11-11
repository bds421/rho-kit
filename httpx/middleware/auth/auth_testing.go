//go:build authtest

package auth

import (
	"context"
	"slices"
)

// WithTrustedS2S returns ctx marked as a trusted service-to-service
// caller.
//
// Available only under the `authtest` build tag — production code
// MUST rely on RequireS2SAuth's mTLS branch to set the marker after
// a verified client certificate. The build tag exists so a typo
// during a refactor cannot promote a test-only bypass into a binary
// shipping to production: opting in to the tag is a deliberate act
// at the build command line.
func WithTrustedS2S(ctx context.Context) context.Context {
	return trustedS2SKey.Set(ctx, trustedS2SMarker{})
}

// WithSubject returns a new context with the given subject.
//
// Available only under the `authtest` build tag. Production code must rely on
// the JWT or mTLS middleware to set the subject.
func WithSubject(ctx context.Context, id string) context.Context {
	return subjectKey.Set(ctx, authSubject(id))
}

// WithPermissions returns a new context with the given permissions.
//
// Available only under the `authtest` build tag. Production code must rely on
// the JWT middleware to set permissions.
func WithPermissions(ctx context.Context, perms []string) context.Context {
	perms = slices.Clone(perms)
	ctx = permissionsKey.Set(ctx, perms)
	ps := make(permissionSet, len(perms))
	for _, p := range perms {
		ps[p] = struct{}{}
	}
	return permSetKey.Set(ctx, ps)
}
