package app

import (
	"net/http"

	"github.com/bds421/rho-kit/data/actionlog"
	"github.com/bds421/rho-kit/data/approval"
	"github.com/bds421/rho-kit/data/budget"
	kitflags "github.com/bds421/rho-kit/flags"
	httpxbudget "github.com/bds421/rho-kit/httpx/middleware/budget"
	httpxtenant "github.com/bds421/rho-kit/httpx/middleware/tenant"
)

// tenantSpec captures everything WithMultiTenant configures so the
// public-mux assembly can install the middleware on inbound requests.
type tenantSpec struct {
	extractor                       httpxtenant.Extractor
	required                        bool
	allowMissingTenantOnSafeMethods bool
}

// budgetSpec captures everything WithTenantBudget configures.
type budgetSpec struct {
	store budget.Budget
	opts  []httpxbudget.Option
}

// tenantMiddleware returns the tenant-extraction middleware for the
// public mux when [WithMultiTenant] has been called, else nil.
func (b *Builder) tenantMiddleware() func(http.Handler) http.Handler {
	if b.tenantSpec == nil {
		return nil
	}
	opts := []httpxtenant.Option{}
	if b.tenantSpec.extractor != nil {
		opts = append(opts, httpxtenant.WithExtractor(b.tenantSpec.extractor))
	}
	opts = append(opts, httpxtenant.WithRequired(b.tenantSpec.required))
	if b.tenantSpec.allowMissingTenantOnSafeMethods {
		opts = append(opts, httpxtenant.WithAllowMissingTenantOnSafeMethods())
	}
	return httpxtenant.New(opts...)
}

// budgetMiddleware returns the inbound budget-enforcement middleware
// for the public mux when [WithTenantBudget] has been called, else
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

// flagsClient builds a flags.Client around the registered provider
// the first time it is requested, returning nil when no provider was
// registered. Callers receive a fresh client per Builder.Run because
// the client wraps an OpenFeature SDK client whose lifecycle is tied
// to the service process.
func (b *Builder) flagsClient() *kitflags.Client {
	if b.flagsProvider == nil {
		return nil
	}
	return kitflags.New(b.name, b.flagsProvider)
}

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
