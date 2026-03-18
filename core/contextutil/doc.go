// Package contextutil provides type-safe, generic context keys.
//
// Each [Key] type parameter forms a unique context key, preventing collisions
// without manually defining sentinel types:
//
//	var UserKey contextutil.Key[User]
//	ctx = UserKey.Set(ctx, currentUser)
//	user, ok := UserKey.Get(ctx)
//
// If multiple values of the same underlying type are needed, define named types:
//
//	type UserID string
//	type SessionID string
//	var userIDKey contextutil.Key[UserID]
//	var sessionIDKey contextutil.Key[SessionID]
package contextutil
