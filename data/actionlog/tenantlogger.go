package actionlog

import (
	"context"
)

// TenantLogger wraps a [Logger] and enforces a single TenantID on every
// operation. Mirrors [data/approval.TenantStore] (FR-054): a bare
// Logger.Get takes only an id with no tenant predicate — a textbook
// IDOR footgun when handlers map GET /audit/{id} onto Logger.Get.
//
// Construct one wrapper per tenant ID at the request boundary:
//
//	scoped := actionlog.NewTenantLogger(logger, tenantID)
//	e, err := scoped.Get(ctx, id) // automatically tenant-scoped
//
// Cross-tenant Get returns [ErrNotFound] (not a distinct mismatch error)
// so a caller cannot probe the existence of another tenant's entries.
type TenantLogger struct {
	inner    Logger
	tenantID string
}

// NewTenantLogger returns a Logger that scopes every operation to
// tenantID. Panics if tenantID is empty — a blank tenant scope would
// silently disable the protection this type exists to provide.
func NewTenantLogger(inner Logger, tenantID string) *TenantLogger {
	if inner == nil {
		panic("actionlog: NewTenantLogger requires a non-nil inner Logger")
	}
	if tenantID == "" {
		panic("actionlog: NewTenantLogger requires a non-empty tenantID")
	}
	return &TenantLogger{inner: inner, tenantID: tenantID}
}

// Append overrides e.TenantID with the wrapper's tenant so a caller
// cannot write into another tenant's hash chain.
func (t *TenantLogger) Append(ctx context.Context, e Entry) (Entry, error) {
	if err := t.ready(); err != nil {
		return Entry{}, err
	}
	e.TenantID = t.tenantID
	return t.inner.Append(ctx, e)
}

// Get returns the entry iff it belongs to the wrapper's tenant.
// Returns [ErrNotFound] for cross-tenant reads so existence cannot be
// probed across tenants.
func (t *TenantLogger) Get(ctx context.Context, id string) (Entry, error) {
	if err := t.ready(); err != nil {
		return Entry{}, err
	}
	e, err := t.inner.Get(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	if e.TenantID != t.tenantID {
		return Entry{}, ErrNotFound
	}
	return e, nil
}

// List substitutes the wrapper's TenantID and clears AllTenants so a
// caller cannot widen the scope from a tenant-scoped boundary.
func (t *TenantLogger) List(ctx context.Context, q Query) ([]Entry, string, error) {
	if err := t.ready(); err != nil {
		return nil, "", err
	}
	q.TenantID = t.tenantID
	q.AllTenants = false
	return t.inner.List(ctx, q)
}

// VerifyChain always verifies the wrapper's tenant chain, ignoring the
// tenantID argument so a caller cannot verify (and learn about) another
// tenant's chain through a scoped logger. The tenantID argument is
// retained for [Logger] interface compatibility.
func (t *TenantLogger) VerifyChain(ctx context.Context, _ string) error {
	if err := t.ready(); err != nil {
		return err
	}
	return t.inner.VerifyChain(ctx, t.tenantID)
}

// Compile-time check that TenantLogger satisfies Logger.
var _ Logger = (*TenantLogger)(nil)

func (t *TenantLogger) ready() error {
	if t == nil || t.inner == nil || t.tenantID == "" {
		return ErrInvalidStore
	}
	return nil
}
