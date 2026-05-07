// Package tenant defines the kit-canonical tenant-ID type and
// context-propagation helpers used by every multi-tenant integration
// (HTTP/gRPC middleware, cache key prefixes, idempotency scoping,
// per-tenant rate limits).
//
// Why a distinct type over `string`? Two reasons:
//
//   - Compile-time isolation: a function declaring `tenant.ID` cannot
//     accidentally accept a user ID, an organisation slug, or any
//     other string-typed identifier.
//   - Refactor safety: every site that consumes a tenant is grep-able
//     from the type, not from a comment naming convention.
//
// The package has zero dependencies on the rest of the kit so any
// module — including those that don't use HTTP at all — can take a
// tenant.ID without an import-graph weight.
package tenant

import (
	"context"
	"errors"
)

// ID identifies a tenant. Construct via [NewID] (validates non-empty)
// or accept the zero-validation responsibility by string-converting.
type ID string

// String returns the tenant ID's underlying string form. Implemented
// so [ID] satisfies fmt.Stringer for log lines.
func (id ID) String() string { return string(id) }

// IsZero reports whether the tenant ID is unset.
func (id ID) IsZero() bool { return id == "" }

// ErrMissing is returned by [Required] when the context carries no
// tenant ID. Callers may compare with `errors.Is`.
var ErrMissing = errors.New("tenant: required tenant ID is missing from context")

// ErrInvalid is returned by [NewID] when the supplied string would
// produce a zero-value tenant ID.
var ErrInvalid = errors.New("tenant: ID must not be empty")

// NewID validates and returns an ID. Empty input returns [ErrInvalid].
func NewID(s string) (ID, error) {
	if s == "" {
		return "", ErrInvalid
	}
	return ID(s), nil
}

// ctxKey is unexported so consumers cannot bypass the typed helpers.
type ctxKey struct{}

// WithID returns a child context carrying id. Use this in the
// HTTP/gRPC middleware that resolves the tenant from request
// metadata.
func WithID(ctx context.Context, id ID) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the tenant ID stored in ctx and a presence
// flag. Absence is reported via the bool, not via [ErrMissing] —
// callers that need to error use [Required] instead.
func FromContext(ctx context.Context) (ID, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(ctxKey{}).(ID)
	if !ok || v.IsZero() {
		return "", false
	}
	return v, true
}

// Required returns the tenant ID from ctx or [ErrMissing]. Use this
// in tenant-scoped handlers, repositories, and integrations where the
// absence of a tenant is a programming error rather than a recoverable
// condition.
func Required(ctx context.Context) (ID, error) {
	id, ok := FromContext(ctx)
	if !ok {
		return "", ErrMissing
	}
	return id, nil
}
