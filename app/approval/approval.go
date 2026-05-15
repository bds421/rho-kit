// Package approval is the lazy app-module wrapper for the kit's
// [github.com/bds421/rho-kit/data/v2/approval] interface. Services
// pass [approval.Module] to [app.Builder.With] to publish the
// supplied store on [app.Infrastructure]; handlers read it via
// [approval.Store].
//
// IMPORTANT: this module ONLY stores the [approval.Store] for
// handlers to consume. The kit does NOT install the
// [httpx/middleware/approval] middleware on the public mux —
// handlers (or the RouterFunc) must wrap the routes that need
// approval gating themselves because lifecycle attribution
// (tenant/actor extractors, action/resource derivation) is too
// service-specific to wire automatically.
//
//	app.New(name, ver, cfg).
//	    With(approval.Module(myApprovalStore)).
//	    Router(func(infra app.Infrastructure) http.Handler {
//	        store := approval.Store(infra)
//	        mux.Handle("DELETE /v1/users/{id}",
//	            approvalmw.Middleware(store, …)(deleteUser),
//	        )
//	        return router(infra)
//	    }).
//	    Run()
package approval

import (
	"context"

	"github.com/bds421/rho-kit/app/v2"
	kitapproval "github.com/bds421/rho-kit/data/v2/approval"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ModuleName is the registered Module.Name() value.
const ModuleName = "approval"

// ResourceStoreKey is the [app.Infrastructure.Resource] key under
// which [Module] publishes the registered Store.
const ResourceStoreKey = "github.com/bds421/rho-kit/app/approval.store"

// Module returns an [app.Module] that publishes the supplied
// Store on [app.Infrastructure] under [ResourceStoreKey].
//
// Panics if store is nil.
func Module(store kitapproval.Store) app.Module {
	if store == nil {
		panic("app/approval: Module requires a non-nil Store")
	}
	return &approvalModule{store: store}
}

type approvalModule struct {
	store kitapproval.Store
}

func (m *approvalModule) Name() string                                  { return ModuleName }
func (m *approvalModule) Init(_ context.Context, _ app.ModuleContext) error { return nil }
func (m *approvalModule) Populate(infra *app.Infrastructure) {
	infra.SetResource(ResourceStoreKey, m.store)
}
func (m *approvalModule) Stop(_ context.Context) error            { return nil }
func (m *approvalModule) HealthChecks() []health.DependencyCheck { return nil }

// Store returns the approval store registered via [Module], or nil
// if no module was registered.
func Store(infra app.Infrastructure) kitapproval.Store {
	v, ok := infra.Resource(ResourceStoreKey)
	if !ok {
		return nil
	}
	s, _ := v.(kitapproval.Store)
	return s
}
