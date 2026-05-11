// Package contextutil provides type-safe, generic context keys.
//
// Each [Key] is constructed with [NewKey] and given a unique identity, so
// two independently-constructed Key[string] values never collide as
// context keys — even across packages.
//
//	var UserKey = contextutil.NewKey[User]("user")
//	ctx = UserKey.Set(ctx, currentUser)
//	user, ok := UserKey.Get(ctx)
//
// Set normalizes a nil context to context.Background(); Get reports false for
// nil contexts. This keeps helper wrappers fail-closed without requiring every
// caller-side test scaffold to pre-normalize contexts.
//
// The zero value of Key is intentionally not usable; calling Set on an
// unconstructed Key panics. Use NewKey, never `var k Key[T]`.
package contextutil
