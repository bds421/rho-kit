// Package contextutil provides type-safe context keys using generics.
// It eliminates the common pattern of declaring private key types and
// separate Set/Get helper functions for each context value.
package contextutil

import (
	"context"
	"fmt"
)

// Key is a type-safe context key. The zero value is ready to use.
// Keys are distinguished by their type parameter T. If you need multiple
// context values of the same underlying type, you MUST define named types.
// Using Key[string] for two different values will cause silent collisions
// because Go treats all zero-value Key[string] as identical context keys.
//
// CORRECT — distinct named types produce distinct keys:
//
//	type UserID string
//	type SessionID string
//	var userKey contextutil.Key[UserID]       // Key[UserID] ≠ Key[SessionID]
//	var sessionKey contextutil.Key[SessionID]
//
// WRONG — both are Key[string], which is the same context key:
//
//	var userKey contextutil.Key[string]    // collision!
//	var sessionKey contextutil.Key[string] // same key as userKey
//
// The Go type system enforces Key[A] ≠ Key[B] when A ≠ B, but cannot
// prevent two packages from independently declaring Key[string]. Always
// use domain-specific named types (even for string/int) to guarantee
// uniqueness.
type Key[T any] struct{}

// Set returns a copy of ctx carrying val.
func (k Key[T]) Set(ctx context.Context, val T) context.Context {
	return context.WithValue(ctx, k, val)
}

// Get retrieves the value associated with k from ctx. The second return value
// reports whether the key was present.
func (k Key[T]) Get(ctx context.Context) (T, bool) {
	val, ok := ctx.Value(k).(T)
	return val, ok
}

// MustGet retrieves the value associated with k from ctx and panics if the key
// is not present.
func (k Key[T]) MustGet(ctx context.Context) T {
	val, ok := k.Get(ctx)
	if !ok {
		var zero T
		panic(fmt.Sprintf("contextutil: Key[%T] not found in context; ensure the value was set upstream and named types are used to avoid collisions", zero))
	}
	return val
}
