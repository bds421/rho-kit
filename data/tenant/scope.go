package tenant

import (
	"context"
	"errors"
	"fmt"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
)

// ErrAnonymousScope is returned by [FromContext] when the request
// context carries no tenant ID. Services that intentionally accept
// anonymous traffic must check for this sentinel and provide their
// own fallback (typically rejecting the call with apperror.AuthRequired).
var ErrAnonymousScope = errors.New("data/tenant: context has no tenant ID")

// Scope binds operations to a single [coretenant.ID]. Constructors
// in data backends accept a Scope rather than reading the tenant ID
// out of an ambient context — this is what makes "forgot the tenant
// filter" a compile-time error.
//
// Zero value is not usable. Construct via [NewScope] or [FromContext].
type Scope struct {
	id coretenant.ID
}

// NewScope returns a Scope for id. Returns an error if id is the
// zero value (use ErrAnonymousScope-style handling at the call
// site if your service has an opt-in anonymous path).
func NewScope(id coretenant.ID) (Scope, error) {
	if id.IsZero() {
		return Scope{}, ErrAnonymousScope
	}
	return Scope{id: id}, nil
}

// MustNewScope panics on a zero tenant.ID. Intended for tests and
// startup-time wiring where the absence of a tenant is a programmer
// bug, not a runtime condition.
func MustNewScope(id coretenant.ID) Scope {
	s, err := NewScope(id)
	if err != nil {
		panic(fmt.Sprintf("data/tenant: MustNewScope: %v", err))
	}
	return s
}

// FromContext extracts the tenant.ID from ctx via
// [coretenant.FromContext] and constructs a Scope. Returns
// [ErrAnonymousScope] when the context carries no tenant.
func FromContext(ctx context.Context) (Scope, error) {
	id, ok := coretenant.FromContext(ctx)
	if !ok {
		return Scope{}, ErrAnonymousScope
	}
	return NewScope(id)
}

// ID returns the underlying tenant.ID. Read-only access — callers
// cannot replace the Scope's bound tenant.
func (s Scope) ID() coretenant.ID { return s.id }

// IsZero reports whether the Scope was constructed via the zero
// value (i.e. not via NewScope/FromContext). Useful in tests and
// defensive checks.
func (s Scope) IsZero() bool { return s.id.IsZero() }

// WhereClause returns a Postgres-shaped tenant filter and the
// positional arg to append to a query's args slice. The current
// placeholder count (i.e. len(args) BEFORE appending the tenant
// arg) is supplied so the helper can produce the correct $N. The
// returned arg is the tenant ID as a string — pgx accepts string
// for both TEXT and UUID columns when the column type is declared
// appropriately on the schema.
//
// Example:
//
//	args := []any{userID, since}
//	clause, tenantArg := scope.WhereClause(len(args))
//	args = append(args, tenantArg)
//	sql := "SELECT ... WHERE user_id = $1 AND created_at >= $2 AND " + clause
//
// Adapters that build queries with positional placeholders should
// prefer this over hand-rolled string concatenation; the function
// is a one-line guard against off-by-one mistakes in the
// placeholder counter.
//
// Panics if currentArgCount is negative: a query cannot have fewer
// than zero existing placeholders, so a negative count is a
// programmer bug. Failing loud here matches the package's fail-loud
// style ([MustNewScope]) and avoids silently emitting an
// out-of-range "$0"/"$-1" that only surfaces later as an obscure
// pgx error at query time.
func (s Scope) WhereClause(currentArgCount int) (clause string, tenantArg any) {
	if currentArgCount < 0 {
		panic(fmt.Sprintf("data/tenant: WhereClause: negative currentArgCount %d", currentArgCount))
	}
	return fmt.Sprintf("tenant_id = $%d", currentArgCount+1), s.id.String()
}

// Key prefixes parts with the tenant ID using the same safe-character
// contract as [coretenant.KeyFor]. The result is a single string
// suitable for use as a Redis cache key, an idempotency key, or a
// lock key — without the caller having to remember the prefix
// convention each time.
//
// Returns an error if any part contains characters
// [coretenant.KeyFor] rejects.
func (s Scope) Key(parts ...string) (string, error) {
	return coretenant.KeyFor(s.id, parts...)
}
