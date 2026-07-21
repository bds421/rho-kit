package approval

import (
	"context"
	"errors"
)

// ErrTenantMismatch is returned by [TenantStore] mutations when the
// stored request belongs to a different tenant than the one the
// wrapper is scoped to. Translates to 404 (not 403) at the HTTP
// boundary so a caller cannot probe for the existence of an
// approval that belongs to a sibling tenant.
//
// Kit backends that implement [TenantScopedMutator] return
// [ErrNotFound] for cross-tenant mutations instead of this sentinel
// (existence must not leak). ErrTenantMismatch remains exported for
// callers and tests that still match it.
var ErrTenantMismatch = errors.New("approval: request does not belong to the scoped tenant")

// TenantScopedMutator is implemented by backends that can push the
// tenant predicate into the same statement that performs the state
// transition (closing the Get-then-mutate TOCTOU window).
//
// [NewTenantStore] requires this capability: check-then-act fallbacks
// are not supported because a concurrent TenantID reassignment can
// commit a decision against the wrong tenant before a post-write
// tripwire observes the mismatch.
type TenantScopedMutator interface {
	ApproveForTenant(ctx context.Context, tenantID, id, decidedBy, reason string) (Request, error)
	RejectForTenant(ctx context.Context, tenantID, id, decidedBy, reason string) (Request, error)
	MarkExecutedForTenant(ctx context.Context, tenantID, id string) (Request, error)
}

// TenantStore wraps a [Store] that also implements [TenantScopedMutator]
// and enforces a single TenantID on every Get / Decide / MarkExecuted
// call (audit FR-054). Pre-fix these mutations took only a request
// id and the underlying SQL used WHERE id = $1 with no tenant
// predicate — a textbook IDOR footgun.
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
// Mutations always go through ApproveForTenant / RejectForTenant /
// MarkExecutedForTenant so the tenant predicate and state transition
// share one call. Kit backends ([memory], [postgres]) implement those
// methods; third-party Store implementations must implement
// [TenantScopedMutator] or [NewTenantStore] panics.
type TenantStore struct {
	inner    Store
	mutator  TenantScopedMutator
	tenantID string
}

// NewTenantStore returns a Store that scopes every operation to
// tenantID. Panics if tenantID is empty, inner is nil, or inner does
// not implement [TenantScopedMutator] — a blank tenant scope or a
// non-atomic backend would silently disable the protection this type
// exists to provide.
func NewTenantStore(inner Store, tenantID string) *TenantStore {
	if inner == nil {
		panic("approval: NewTenantStore requires a non-nil inner Store")
	}
	if tenantID == "" {
		panic("approval: NewTenantStore requires a non-empty tenantID")
	}
	mutator, ok := inner.(TenantScopedMutator)
	if !ok {
		panic("approval: NewTenantStore requires a Store implementing TenantScopedMutator (ApproveForTenant/RejectForTenant/MarkExecutedForTenant)")
	}
	return &TenantStore{inner: inner, mutator: mutator, tenantID: tenantID}
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

// Approve enforces tenant ownership via ApproveForTenant.
// Returns [ErrNotFound] when the request belongs to another tenant.
func (t *TenantStore) Approve(ctx context.Context, id, decidedBy, reason string) (Request, error) {
	if err := t.ready(); err != nil {
		return Request{}, err
	}
	return t.mutator.ApproveForTenant(ctx, t.tenantID, id, decidedBy, reason)
}

// Reject enforces tenant ownership via RejectForTenant.
// Returns [ErrNotFound] when the request belongs to another tenant.
func (t *TenantStore) Reject(ctx context.Context, id, decidedBy, reason string) (Request, error) {
	if err := t.ready(); err != nil {
		return Request{}, err
	}
	return t.mutator.RejectForTenant(ctx, t.tenantID, id, decidedBy, reason)
}

// MarkExecuted enforces tenant ownership via MarkExecutedForTenant.
func (t *TenantStore) MarkExecuted(ctx context.Context, id string) (Request, error) {
	if err := t.ready(); err != nil {
		return Request{}, err
	}
	return t.mutator.MarkExecutedForTenant(ctx, t.tenantID, id)
}

// Compile-time check that TenantStore satisfies the Store interface
// so it drops into anywhere the kit accepts a Store.
var _ Store = (*TenantStore)(nil)

func (t *TenantStore) ready() error {
	if t == nil || t.inner == nil || t.mutator == nil || t.tenantID == "" {
		return ErrInvalidStore
	}
	return nil
}
