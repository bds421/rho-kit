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
	"fmt"
	"strings"
)

// ID identifies a tenant. Construct via [NewID] (validates the input)
// or, for a value already validated upstream (e.g. read from a trusted
// DB column), [NewIDUnchecked].
//
// Allowed characters in a tenant ID: any byte *except* the separators
// and control codes used by the rest of the kit. Specifically rejected:
//
//   - ':' — reserved as the field separator in cache / idempotency
//     scoped keys. Allowing ':' would let `tenant:"a:b" + key:"c"`
//     collide with `tenant:"a" + key:"b:c"` (silent cross-tenant leak).
//   - '/' — reserved for path-like keying schemes and operator URLs.
//   - '\n', '\r', '\t', '\x00' — control codes that corrupt log lines,
//     header values, and Redis MONITOR traces.
//
// All other bytes (alphanumerics, '-', '_', '.', UUIDs) are accepted.
// Length is bounded to [MaxIDLen] bytes — long enough to fit any
// reasonable opaque identifier (UUIDs, ULIDs, KSUIDs, hashed slugs)
// while keeping log lines, header values, and cache keys bounded so a
// malicious header cannot drive cache-key, log, or metric blow-up.
type ID string

// MaxIDLen is the maximum length, in bytes, of a tenant ID accepted by
// [ValidateID]. The cap is intentionally generous (256 bytes) so it
// doesn't reject UUIDs, hierarchical org/tenant slugs, or hashed
// composite keys, while still bounding the size of cache prefixes,
// log lines, and metric labels that incorporate the tenant ID.
const MaxIDLen = 256

// String returns the tenant ID's underlying string form. Implemented
// so [ID] satisfies fmt.Stringer for log lines.
func (id ID) String() string { return string(id) }

// IsZero reports whether the tenant ID is unset.
func (id ID) IsZero() bool { return id == "" }

// ErrMissing is returned by [Required] when the context carries no
// tenant ID. Callers may compare with `errors.Is`.
var ErrMissing = errors.New("tenant: required tenant ID is missing from context")

// ErrInvalid is returned by [NewID] when the supplied string fails
// validation (empty or contains a forbidden byte). Callers may compare
// with `errors.Is`.
var ErrInvalid = errors.New("tenant: ID is invalid")

// forbiddenBytes lists every byte that [ValidateID] rejects. The
// separator ':' is the load-bearing one — see the package doc for the
// full rationale.
const forbiddenBytes = ":/\n\r\t\x00"

// ValidateID reports whether s is a well-formed tenant ID. Returns nil
// on success, an error wrapping [ErrInvalid] otherwise. Callers that
// need the validated ID should use [NewID]; ValidateID is exposed for
// callers that want to validate input before passing it through other
// layers (e.g. an HTTP middleware that wants a 400 response, not a
// panic, on bad input).
func ValidateID(s string) error {
	if s == "" {
		return fmt.Errorf("%w: must not be empty", ErrInvalid)
	}
	if len(s) > MaxIDLen {
		return fmt.Errorf("%w: length %d exceeds maximum %d bytes", ErrInvalid, len(s), MaxIDLen)
	}
	if i := strings.IndexAny(s, forbiddenBytes); i >= 0 {
		return fmt.Errorf("%w: contains forbidden byte %q at offset %d", ErrInvalid, s[i], i)
	}
	return nil
}

// NewID validates s with [ValidateID] and returns the corresponding ID
// on success. The returned error wraps [ErrInvalid] so callers can use
// `errors.Is(err, ErrInvalid)`.
func NewID(s string) (ID, error) {
	if err := ValidateID(s); err != nil {
		return "", err
	}
	return ID(s), nil
}

// NewIDUnchecked converts s into an ID without validation. Use only
// when s has been validated upstream — typical case is reading from a
// trusted database column populated via [NewID]. The empty string is
// still allowed; callers that want non-empty must check [ID.IsZero].
//
// This is the documented escape hatch for backwards compatibility with
// stored data that pre-dates the [ValidateID] tightening. New code
// paths should prefer [NewID].
func NewIDUnchecked(s string) ID { return ID(s) }

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
