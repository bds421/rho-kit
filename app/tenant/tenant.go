// Package tenant is the lazy app-module wrapper for the kit's
// tenant-extraction middleware
// ([github.com/bds421/rho-kit/httpx/v2/middleware/tenant]). Services
// pass [tenant.Module] to [app.Builder.With] to install the
// middleware at [app.PhaseTenant] on the public mux.
//
//	app.New(name, ver, cfg).
//	    With(tenant.Module(httpxtenant.HeaderExtractor("X-Tenant-Id"))).
//	    Router(routerFn).
//	    Run()
//
// The kit insists on an affirmative declaration:
//   - `tenant.Module(extractor)` requires the tenant on every
//     request (default). Requests without a tenant get 400.
//   - `tenant.Module(extractor, tenant.WithoutTenantRequired())`
//     extracts when present, allows absence — for hybrid services.
//   - `tenant.Module(extractor, tenant.WithAllowMissingOnSafeMethods())`
//     relaxes Required for GET/HEAD/OPTIONS only.
//
// The extractor defaults to [httpxtenant.ContextExtractor] when nil,
// which reads a tenant attached by an upstream authentication
// middleware (PASETO/JWT verifier, mTLS subject mapper). Pass
// [httpxtenant.HeaderExtractor] to trust X-Tenant-Id.
//
// The bridge does not expose an accessor — once registered, the
// middleware runs automatically and handlers reach for
// `coretenant.FromContext(r.Context())` to read the extracted ID.
package tenant

import (
	"context"
	"net/http"

	"github.com/bds421/rho-kit/app/v2"
	httpxtenant "github.com/bds421/rho-kit/httpx/v2/middleware/tenant"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ModuleName is the registered Module.Name() value.
const ModuleName = "tenant"

// Option configures [Module].
type Option func(*config)

type config struct {
	extractor                 httpxtenant.Extractor
	required                  bool
	allowMissingOnSafeMethods bool
}

// WithoutTenantRequired makes tenant optional rather than required.
// The extractor is still applied — present tenants flow to ctx — but
// requests without a tenant are not rejected. Mutually exclusive
// with [budget.Module].
func WithoutTenantRequired() Option {
	return func(c *config) { c.required = false }
}

// WithAllowMissingOnSafeMethods opts out of require-tenant-on-every-method
// for GET/HEAD/OPTIONS. Only meaningful when paired with the default
// Required policy. Mutually exclusive with [budget.Module] (budget
// enforcement needs a tenant on every charged request).
func WithAllowMissingOnSafeMethods() Option {
	return func(c *config) { c.allowMissingOnSafeMethods = true }
}

// Module returns an [app.Module] that registers the tenant-
// extraction middleware at [app.PhaseTenant]. The default policy
// is Required; pass [WithoutTenantRequired] to relax.
func Module(extractor httpxtenant.Extractor, opts ...Option) app.Module {
	cfg := config{extractor: extractor, required: true}
	for _, opt := range opts {
		if opt == nil {
			panic("app/tenant: Module option must not be nil")
		}
		opt(&cfg)
	}
	return &tenantModule{cfg: cfg}
}

type tenantModule struct {
	cfg config
	// middleware is cached in Init so PublicMiddleware callers get
	// a stable function value.
	middleware func(http.Handler) http.Handler
}

func (m *tenantModule) Name() string { return ModuleName }

func (m *tenantModule) Init(_ context.Context, _ app.ModuleContext) error {
	opts := []httpxtenant.Option{}
	if m.cfg.extractor != nil {
		opts = append(opts, httpxtenant.WithExtractor(m.cfg.extractor))
	}
	if !m.cfg.required {
		opts = append(opts, httpxtenant.WithoutTenantRequired())
	}
	if m.cfg.allowMissingOnSafeMethods {
		opts = append(opts, httpxtenant.WithAllowMissingTenantOnSafeMethods())
	}
	m.middleware = httpxtenant.New(opts...)
	return nil
}

func (m *tenantModule) Populate(_ *app.Infrastructure) {}

// Stop is a no-op; the tenant middleware has no lifecycle.
func (m *tenantModule) Stop(_ context.Context) error { return nil }

func (m *tenantModule) HealthChecks() []health.DependencyCheck { return nil }

// PublicMiddleware satisfies [app.MiddlewareInstaller]. The
// middleware is installed at [app.PhaseTenant]. The middleware
// function is constructed once in [Init] and cached.
func (m *tenantModule) PublicMiddleware() []app.PhasedMiddleware {
	if m.middleware == nil {
		return nil
	}
	return []app.PhasedMiddleware{{
		Phase: app.PhaseTenant,
		Func:  m.middleware,
	}}
}

// TenantRequired satisfies [app.TenantPolicyProvider]. Used by
// the budget bridge at Init time to verify Required policy.
func (m *tenantModule) TenantRequired() bool { return m.cfg.required }

// TenantAllowsMissingOnSafeMethods satisfies
// [app.TenantPolicyProvider].
func (m *tenantModule) TenantAllowsMissingOnSafeMethods() bool {
	return m.cfg.allowMissingOnSafeMethods
}
