package app

import (
	"net/http"

	"github.com/bds421/rho-kit/data/v2/actionlog"
	"github.com/bds421/rho-kit/data/v2/approval"
	"github.com/bds421/rho-kit/data/v2/budget"
	httpxbudget "github.com/bds421/rho-kit/httpx/v2/middleware/budget"
	httpxtenant "github.com/bds421/rho-kit/httpx/v2/middleware/tenant"
)

// tenantSpec captures everything MultiTenant configures so the
// public-mux assembly can install the middleware on inbound requests.
type tenantSpec struct {
	extractor                       httpxtenant.Extractor
	required                        bool
	allowMissingTenantOnSafeMethods bool
}

// budgetSpec captures everything TenantBudget configures.
type budgetSpec struct {
	store budget.Budget
	opts  []httpxbudget.Option
}

// tenantMiddleware returns the tenant-extraction middleware for the
// public mux when [MultiTenant] has been called, else nil.
func (b *Builder) tenantMiddleware() func(http.Handler) http.Handler {
	if b.tenantSpec == nil {
		return nil
	}
	opts := []httpxtenant.Option{}
	if b.tenantSpec.extractor != nil {
		opts = append(opts, httpxtenant.WithExtractor(b.tenantSpec.extractor))
	}
	if !b.tenantSpec.required {
		opts = append(opts, httpxtenant.WithoutTenantRequired())
	}
	if b.tenantSpec.allowMissingTenantOnSafeMethods {
		opts = append(opts, httpxtenant.WithAllowMissingTenantOnSafeMethods())
	}
	return httpxtenant.New(opts...)
}

// budgetMiddleware returns the inbound budget-enforcement middleware
// for the public mux when [TenantBudget] has been called, else
// nil.
func (b *Builder) budgetMiddleware() func(http.Handler) http.Handler {
	if b.budgetSpec == nil {
		return nil
	}
	return httpxbudget.Middleware(b.budgetSpec.store, b.budgetSpec.opts...)
}

// actionLogger returns the registered logger or nil.
func (b *Builder) actionLogger() actionlog.Logger { return b.alog }

// approvalStore returns the registered store or nil.
func (b *Builder) approvalStore() approval.Store { return b.astore }

// budgetSpecStore returns the registered budget store or nil. The
// helper exists so Infrastructure population stays a single-line
// per field; the spec wrapper holds opts that aren't part of the
// public Infrastructure shape.
func (b *Builder) budgetSpecStore() budget.Budget {
	if b.budgetSpec == nil {
		return nil
	}
	return b.budgetSpec.store
}
