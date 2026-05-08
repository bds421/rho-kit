// Package contextutil provides type-safe context keys using generics.
// It eliminates the common pattern of declaring private key types and
// separate Set/Get helper functions for each context value.
package contextutil

import (
	"context"
	"fmt"
)

// Key is a type-safe context key. Construct one with [NewKey] and store
// it in a package-level variable; the zero value is intentionally not
// usable.
//
// Each NewKey call returns a key with a unique identity, so two
// independently-constructed Key[string] values never collide — even
// across packages. This was the primary footgun of the v1 zero-value
// design and is closed off by requiring construction.
//
// Recommended pattern:
//
//	var userKey = contextutil.NewKey[User]("user")
//	var sessionKey = contextutil.NewKey[Session]("session")
//
//	ctx = userKey.Set(ctx, u)
//	u, ok := userKey.Get(ctx)
type Key[T any] struct {
	// id points to a unique per-Key sentinel. Two Key[T] with the same
	// T compare equal as context keys iff they share the same id, which
	// only happens when one was copied from the other.
	id *keyID
}

// keyID is a unique-per-NewKey-call sentinel. Its address is the actual
// context key; the embedded name is purely for diagnostics.
type keyID struct {
	name string
}

// NewKey constructs a context key for type T. Pass a short, descriptive
// name (used only for error messages — it does not need to be globally
// unique). Each call returns a key with a fresh identity, so two
// NewKey[string] calls produce two distinct context keys.
func NewKey[T any](name string) Key[T] {
	return Key[T]{id: &keyID{name: name}}
}

// Set returns a copy of ctx carrying val. Panics if k was not
// constructed with [NewKey].
func (k Key[T]) Set(ctx context.Context, val T) context.Context {
	if k.id == nil {
		panic("contextutil: Key was not constructed with NewKey — its identity would collide with every other zero-value Key")
	}
	return context.WithValue(ctx, k.id, val)
}

// Get retrieves the value associated with k from ctx. The second return
// value reports whether the key was present.
func (k Key[T]) Get(ctx context.Context) (T, bool) {
	var zero T
	if k.id == nil {
		return zero, false
	}
	val, ok := ctx.Value(k.id).(T)
	return val, ok
}

// MustGet retrieves the value associated with k from ctx and panics if
// the key is not present.
func (k Key[T]) MustGet(ctx context.Context) T {
	val, ok := k.Get(ctx)
	if !ok {
		var zero T
		name := "<anonymous>"
		if k.id != nil {
			name = k.id.name
		}
		panic(fmt.Sprintf("contextutil: Key[%T] %q not found in context; ensure the value was set upstream", zero, name))
	}
	return val
}
