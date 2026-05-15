// Package budget is the lazy app-module wrapper for the kit's
// per-tenant cost-budget middleware
// ([github.com/bds421/rho-kit/httpx/v2/middleware/budget]).
// Services pass [budget.Module] to [app.Builder.With] to install
// the middleware at [app.PhaseBudget] on the public mux.
//
//	app.New(name, ver, cfg).
//	    With(tenant.Module(extractor)).          // Required
//	    With(budget.Module(myBudgetStore)).      // enforces per-tenant cost
//	    Router(func(infra app.Infrastructure) http.Handler {
//	        store := budget.Store(infra)
//	        // attach to admin endpoints for read-or-override
//	        return router(infra, store)
//	    }).
//	    Run()
//
// Budget enforcement keys on the tenant ID stored on the request
// context, so [tenant.Module] must also be registered and configured
// with the default Required policy. [budget.Module]'s Init returns
// an actionable error if either constraint is violated; this catches
// the misconfiguration at startup rather than at request time.
package budget

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bds421/rho-kit/app/v2"
	apptenant "github.com/bds421/rho-kit/app/tenant/v2"
	"github.com/bds421/rho-kit/data/v2/budget"
	httpxbudget "github.com/bds421/rho-kit/httpx/v2/middleware/budget"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ModuleName is the registered Module.Name() value.
const ModuleName = "budget"

// ResourceStoreKey is the [app.Infrastructure.Resource] key under
// which [Module] publishes the registered Budget store. Handlers
// that need to read or override the budget directly can access it
// via [Store].
const ResourceStoreKey = "github.com/bds421/rho-kit/app/budget.store"

// Module returns an [app.Module] that registers the budget-
// enforcement middleware at [app.PhaseBudget]. The middleware
// charges every request against the per-tenant bucket b. Custom
// keying can be supplied via [httpxbudget.WithKeyFunc] in opts.
//
// Init returns an error if [tenant.Module] is not also registered,
// or if it was registered with a non-Required policy
// ([tenant.WithoutTenantRequired] or
// [tenant.WithAllowMissingOnSafeMethods]) — budget enforcement
// needs a tenant on every charged request.
//
// Panics if b is nil or any opt is nil.
func Module(b budget.Budget, opts ...httpxbudget.Option) app.Module {
	if b == nil {
		panic("app/budget: Module requires a non-nil budget store")
	}
	for _, opt := range opts {
		if opt == nil {
			panic("app/budget: Module option must not be nil")
		}
	}
	cloned := append([]httpxbudget.Option(nil), opts...)
	return &budgetModule{store: b, opts: cloned}
}

type budgetModule struct {
	store budget.Budget
	opts  []httpxbudget.Option
	// middleware is cached in Init so PublicMiddleware callers get a
	// stable function value across repeated reads — the kit's
	// middleware-chain builder is allowed to read this more than once.
	middleware func(http.Handler) http.Handler
}

func (m *budgetModule) Name() string { return ModuleName }

func (m *budgetModule) Init(_ context.Context, mc app.ModuleContext) error {
	tm := mc.LookupModule(apptenant.ModuleName)
	if tm == nil {
		return fmt.Errorf("app/budget: budget requires app/tenant.Module so the default budget key can be derived from the tenant context")
	}
	tp, ok := tm.(app.TenantPolicyProvider)
	if !ok {
		// Defensive: only the app/tenant.Module type implements
		// this interface, but guard against a third-party module
		// using the same Name().
		return fmt.Errorf("app/budget: registered tenant module (%q) does not implement TenantPolicyProvider — use app/tenant.Module", apptenant.ModuleName)
	}
	if !tp.TenantRequired() {
		return fmt.Errorf("app/budget: budget requires tenant.Module without tenant.WithoutTenantRequired() because budget enforcement needs a tenant key on every charged request")
	}
	if tp.TenantAllowsMissingOnSafeMethods() {
		return fmt.Errorf("app/budget: budget is incompatible with tenant.WithAllowMissingOnSafeMethods() because budget enforcement needs a tenant key on every charged request")
	}
	m.middleware = httpxbudget.Middleware(m.store, m.opts...)
	return nil
}

func (m *budgetModule) Populate(infra *app.Infrastructure) {
	infra.SetResource(ResourceStoreKey, m.store)
}

// Stop is a no-op; the underlying budget.Budget has no lifecycle.
func (m *budgetModule) Stop(_ context.Context) error { return nil }

func (m *budgetModule) HealthChecks() []health.DependencyCheck { return nil }

// PublicMiddleware satisfies [app.MiddlewareInstaller]. The
// middleware is installed at [app.PhaseBudget]. The middleware
// function is constructed once in [Init] and cached so repeated
// PublicMiddleware reads return the same value.
func (m *budgetModule) PublicMiddleware() []app.PhasedMiddleware {
	if m.middleware == nil {
		return nil
	}
	return []app.PhasedMiddleware{{
		Phase: app.PhaseBudget,
		Func:  m.middleware,
	}}
}

// Store returns the budget store registered via [Module], or nil
// if no module was registered.
func Store(infra app.Infrastructure) budget.Budget {
	v, ok := infra.Resource(ResourceStoreKey)
	if !ok {
		return nil
	}
	s, _ := v.(budget.Budget)
	return s
}
