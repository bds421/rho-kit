// Package auditlog is the lazy app-module wrapper for the kit's
// [github.com/bds421/rho-kit/observability/v2/auditlog] logger.
// Services pass [auditlog.Module] to [app.Builder.With] to wire an
// audit logger backed by the supplied store; handlers access the
// resulting *auditlog.Logger via [auditlog.Logger].
//
//	app.New(name, ver, cfg).
//	    With(auditlog.Module(auditlog.NewMemoryStore())).
//	    Router(func(infra app.Infrastructure) http.Handler {
//	        logger := auditlog.Logger(infra)
//	        // attach to handlers, middleware, or domain events
//	        return router(infra)
//	    }).
//	    Run()
//
// Keeping the audit logger in this bridge keeps the
// observability/v2/auditlog import out of services that do not
// register one — consistent with waves 88-92 (flags, paseto, jwt,
// leader, slo, cron, ratelimit, signedrequest, http, storage).
package auditlog

import (
	"context"

	"github.com/bds421/rho-kit/app/v2"
	kitauditlog "github.com/bds421/rho-kit/observability/v2/auditlog"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ModuleName is the registered Module.Name() value.
const ModuleName = "auditlog"

// ResourceLoggerKey is the [app.Infrastructure.Resource] key under
// which [Module] publishes its *kitauditlog.Logger. Tests can read
// it directly; application code should use [Logger].
const ResourceLoggerKey = "github.com/bds421/rho-kit/app/auditlog.logger"

// Module returns an [app.Module] that constructs a
// *kitauditlog.Logger from store + opts and publishes it on
// [app.Infrastructure] under [ResourceLoggerKey].
//
// Panics if store is nil or any opt is nil.
func Module(store kitauditlog.Store, opts ...kitauditlog.Option) app.Module {
	if store == nil {
		panic("app/auditlog: Module requires a non-nil store")
	}
	for _, opt := range opts {
		if opt == nil {
			panic("app/auditlog: Module option must not be nil")
		}
	}
	cloned := append([]kitauditlog.Option(nil), opts...)
	return &auditlogModule{store: store, opts: cloned}
}

type auditlogModule struct {
	store  kitauditlog.Store
	opts   []kitauditlog.Option
	logger *kitauditlog.Logger
}

func (m *auditlogModule) Name() string { return ModuleName }

func (m *auditlogModule) Init(_ context.Context, _ app.ModuleContext) error {
	m.logger = kitauditlog.New(m.store, m.opts...)
	return nil
}

func (m *auditlogModule) Populate(infra *app.Infrastructure) {
	if m.logger != nil {
		infra.SetResource(ResourceLoggerKey, m.logger)
	}
}

func (m *auditlogModule) Stop(_ context.Context) error            { return nil }
func (m *auditlogModule) HealthChecks() []health.DependencyCheck { return nil }

// Logger returns the audit logger registered via [Module], or nil
// if no audit-log module was registered with the Builder.
func Logger(infra app.Infrastructure) *kitauditlog.Logger {
	v, ok := infra.Resource(ResourceLoggerKey)
	if !ok {
		return nil
	}
	l, _ := v.(*kitauditlog.Logger)
	return l
}
