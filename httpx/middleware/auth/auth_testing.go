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

// WithUserID returns a new context with the given user ID.
//
// Available only under the `authtest` build tag. Production code must rely on
// the JWT or mTLS middleware to set the user ID.
func WithUserID(ctx context.Context, id string) context.Context {
	return userIDKey.Set(ctx, authUserID(id))
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
