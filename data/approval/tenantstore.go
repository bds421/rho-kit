package approval

import (
	"context"
	"errors"
	"fmt"
)

// ErrTenantMismatch is returned by [TenantStore] mutations when the
// stored request belongs to a different tenant than the one the
// wrapper is scoped to. Translates to 404 (not 403) at the HTTP
// boundary so a caller cannot probe for the existence of an
// approval that belongs to a sibling tenant.
var ErrTenantMismatch = errors.New("approval: request does not belong to the scoped tenant")

// TenantStore wraps a [Store] and enforces a single TenantID on
// every Get / Decide / MarkExecuted call (audit FR-054). Pre-fix
// these mutations took only a request id and the underlying SQL
// used WHERE id = $1 with no tenant predicate — a textbook IDOR
// footgun.
//
// Construct one wrapper per tenant ID at the request boundary
// (typically inside an HTTP handler that has already extracted the
// tenant from the request context):
//
//	scoped := approval.NewTenantStore(store, tenantID)
//	r, err := scoped.Get(ctx, id) // automatically tenant-scoped
//
// List callers should still pass [Query.TenantID] explicitly — the
// wrapper substitutes its own tenant on List as well so a stray
// caller cannot widen the query.
//
// # TOCTOU window on state transitions (L057)
//
// Kit backends ([memory], [postgres]) implement the optional
// ApproveForTenant / RejectForTenant / MarkExecutedForTenant methods;
// TenantStore prefers those and pushes the tenant predicate into the
// same mutation, closing the Get-then-mutate window for production
// wiring.
//
// Against a third-party [Store] that only implements the id-only
// mutation methods, TenantStore falls back to check-then-modify: it
// reads the row to confirm ownership, then calls the inner mutation.
// If a concurrent operator action reassigns the row's TenantID
// between the read and the write, the underlying state transition can
// run against the reassigned tenant's record. TenantStore detects this
// by re-reading TenantID after the write and returns ErrTenantMismatch,
// but at that point the state transition (and any audit log entry
// written inside the inner Store) has already happened.
//
// Operators MUST treat in-place TenantID reassignment as a privileged
// administrative action and not a routine API; the post-write check is
// a best-effort tripwire for non-atomic backends, not a primary defense.
type TenantStore struct {
	inner    Store
	tenantID string
}

// NewTenantStore returns a Store that scopes every operation to
// tenantID. Panics if tenantID is empty — a blank tenant scope
// would silently disable the protection this type exists to
// provide.
func NewTenantStore(inner Store, tenantID string) *TenantStore {
	if inner == nil {
		panic("approval: NewTenantStore requires a non-nil inner Store")
	}
	if tenantID == "" {
		panic("approval: NewTenantStore requires a non-empty tenantID")
	}
	return &TenantStore{inner: inner, tenantID: tenantID}
}

// Create overrides r.TenantID with the wrapper's tenantID so a
// caller cannot create an approval request scoped to a different
// tenant than the request boundary they came in on.
func (t *TenantStore) Create(ctx context.Context, r Request) (Request, error) {
	if err := t.ready(); err != nil {
		return Request{}, err
	}
	r.TenantID = t.tenantID
	return t.inner.Create(ctx, r)
}

// Get returns the request iff it belongs to the wrapper's tenant.
// Returns [ErrNotFound] (NOT [ErrTenantMismatch]) for cross-tenant
// reads so a caller cannot probe the existence of approvals owned
// by other tenants.
func (t *TenantStore) Get(ctx context.Context, id string) (Request, error) {
	if err := t.ready(); err != nil {
		return Request{}, err
	}
	r, err := t.inner.Get(ctx, id)
	if err != nil {
		return Request{}, err
	}
	if r.TenantID != t.tenantID {
		return Request{}, ErrNotFound
	}
	return r, nil
}

// List substitutes the wrapper's TenantID into the query so a
// caller cannot widen the scope or set AllTenants from a tenant-
// scoped boundary. The cursor is opaque and passed through unchanged.
func (t *TenantStore) List(ctx context.Context, q Query) ([]Request, string, error) {
	if err := t.ready(); err != nil {
		return nil, "", err
	}
	q.TenantID = t.tenantID
	q.AllTenants = false
	return t.inner.List(ctx, q)
}

// Approve enforces tenant ownership before delegating to the inner
// store. Returns [ErrNotFound] when the request belongs to another
// tenant.
func (t *TenantStore) Approve(ctx context.Context, id, decidedBy, reason string) (Request, error) {
	return t.decideTenant(ctx, id, decidedBy, reason, true)
}

// Reject enforces tenant ownership before delegating to the inner
// store. Returns [ErrNotFound] when the request belongs to another
// tenant.
func (t *TenantStore) Reject(ctx context.Context, id, decidedBy, reason string) (Request, error) {
	return t.decideTenant(ctx, id, decidedBy, reason, false)
}

// tenantScopedMutator is optionally implemented by backends that can push
// the tenant predicate into the same statement that performs the state
// transition (closing the Get-then-mutate TOCTOU on TenantStore).
type tenantScopedMutator interface {
	ApproveForTenant(ctx context.Context, tenantID, id, decidedBy, reason string) (Request, error)
	RejectForTenant(ctx context.Context, tenantID, id, decidedBy, reason string) (Request, error)
	MarkExecutedForTenant(ctx context.Context, tenantID, id string) (Request, error)
}

func (t *TenantStore) decideTenant(ctx context.Context, id, decidedBy, reason string, approve bool) (Request, error) {
	if err := t.ready(); err != nil {
		return Request{}, err
	}
	// Prefer atomic tenant-scoped backends when available.
	if m, ok := t.inner.(tenantScopedMutator); ok {
		if approve {
			return m.ApproveForTenant(ctx, t.tenantID, id, decidedBy, reason)
		}
		return m.RejectForTenant(ctx, t.tenantID, id, decidedBy, reason)
	}
	if _, err := t.Get(ctx, id); err != nil {
		return Request{}, err
	}
	var r Request
	var err error
	if approve {
		r, err = t.inner.Approve(ctx, id, decidedBy, reason)
	} else {
		r, err = t.inner.Reject(ctx, id, decidedBy, reason)
	}
	if err != nil {
		return Request{}, err
	}
	if r.TenantID != t.tenantID {
		// Should not happen if Get returned cleanly; treat as a
		// store-level inconsistency.
		return Request{}, fmt.Errorf("%w: stored tenant changed mid-operation", ErrTenantMismatch)
	}
	return r, nil
}

// MarkExecuted enforces tenant ownership before delegating.
func (t *TenantStore) MarkExecuted(ctx context.Context, id string) (Request, error) {
	if err := t.ready(); err != nil {
		return Request{}, err
	}
	if m, ok := t.inner.(tenantScopedMutator); ok {
		return m.MarkExecutedForTenant(ctx, t.tenantID, id)
	}
	if _, err := t.Get(ctx, id); err != nil {
		return Request{}, err
	}
	r, err := t.inner.MarkExecuted(ctx, id)
	if err != nil {
		return Request{}, err
	}
	if r.TenantID != t.tenantID {
		return Request{}, fmt.Errorf("%w: stored tenant changed mid-operation", ErrTenantMismatch)
	}
	return r, nil
}

// Compile-time check that TenantStore satisfies the Store interface
// so it drops into anywhere the kit accepts a Store.
var _ Store = (*TenantStore)(nil)

func (t *TenantStore) ready() error {
	if t == nil || t.inner == nil || t.tenantID == "" {
		return ErrInvalidStore
	}
	return nil
}
