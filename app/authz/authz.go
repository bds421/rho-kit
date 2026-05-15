// Package authz is the lazy app-module wrapper for the kit's
// vendor-neutral authorization seam
// ([github.com/bds421/rho-kit/authz/v2]). Services pass
// [authz.Module] to [app.Builder.With] to publish a Decider —
// typically an OpenFGA, Cedar, Casbin, or in-memory test adapter —
// on [app.Infrastructure]; handlers read it via [authz.Decider].
//
// The kit does NOT auto-apply authz to the public mux because
// authorization needs per-route subject + resource extractors that
// depend on the route's parameter shape. The middleware lives at
// the route level, not the mux level — typically via
// [httpx/authz.FromDecider] and [httpx/authz.RequirePermission].
//
//	app.New(name, ver, cfg).
//	    With(authz.Module(decider)).
//	    Router(func(infra app.Infrastructure) http.Handler {
//	        d := authz.Decider(infra)
//	        return router(infra, d)
//	    }).
//	    Run()
package authz

import (
	"context"

	"github.com/bds421/rho-kit/app/v2"
	kitauthz "github.com/bds421/rho-kit/authz/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ModuleName is the registered Module.Name() value.
const ModuleName = "authz"

// ResourceDeciderKey is the [app.Infrastructure.Resource] key under
// which [Module] publishes the registered Decider.
const ResourceDeciderKey = "github.com/bds421/rho-kit/app/authz.decider"

// Module returns an [app.Module] that publishes the supplied
// Decider on [app.Infrastructure] under [ResourceDeciderKey].
//
// Panics if decider is nil.
func Module(decider kitauthz.Decider) app.Module {
	if decider == nil {
		panic("app/authz: Module requires a non-nil Decider")
	}
	return &authzModule{decider: decider}
}

type authzModule struct {
	decider kitauthz.Decider
}

func (m *authzModule) Name() string                                  { return ModuleName }
func (m *authzModule) Init(_ context.Context, _ app.ModuleContext) error { return nil }
func (m *authzModule) Populate(infra *app.Infrastructure) {
	infra.SetResource(ResourceDeciderKey, m.decider)
}
func (m *authzModule) Stop(_ context.Context) error            { return nil }
func (m *authzModule) HealthChecks() []health.DependencyCheck { return nil }

// Decider returns the decider registered via [Module], or nil if
// no module was registered.
func Decider(infra app.Infrastructure) kitauthz.Decider {
	v, ok := infra.Resource(ResourceDeciderKey)
	if !ok {
		return nil
	}
	d, _ := v.(kitauthz.Decider)
	return d
}
