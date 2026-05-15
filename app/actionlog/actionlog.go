// Package actionlog is the lazy app-module wrapper for the kit's
// [github.com/bds421/rho-kit/data/v2/actionlog] interface. Services
// pass [actionlog.Module] to [app.Builder.With] to publish the
// supplied logger on [app.Infrastructure]; handlers read it via
// [actionlog.Logger].
//
//	app.New(name, ver, cfg).
//	    With(actionlog.Module(myActionLogger)).
//	    Router(func(infra app.Infrastructure) http.Handler {
//	        alog := actionlog.Logger(infra)
//	        // attach to MCP tool dispatcher, governance middleware, etc.
//	        return router(infra)
//	    }).
//	    Run()
//
// The kit does NOT auto-instrument routes — verb/resource
// attribution is application-specific. Handlers append entries as
// needed.
package actionlog

import (
	"context"

	"github.com/bds421/rho-kit/app/v2"
	kitactionlog "github.com/bds421/rho-kit/data/v2/actionlog"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ModuleName is the registered Module.Name() value.
const ModuleName = "actionlog"

// ResourceLoggerKey is the [app.Infrastructure.Resource] key under
// which [Module] publishes the registered Logger.
const ResourceLoggerKey = "github.com/bds421/rho-kit/app/actionlog.logger"

// Module returns an [app.Module] that publishes the supplied
// Logger on [app.Infrastructure] under [ResourceLoggerKey].
//
// Panics if logger is nil.
func Module(logger kitactionlog.Logger) app.Module {
	if logger == nil {
		panic("app/actionlog: Module requires a non-nil Logger")
	}
	return &actionlogModule{logger: logger}
}

type actionlogModule struct {
	logger kitactionlog.Logger
}

func (m *actionlogModule) Name() string                                  { return ModuleName }
func (m *actionlogModule) Init(_ context.Context, _ app.ModuleContext) error { return nil }
func (m *actionlogModule) Populate(infra *app.Infrastructure) {
	infra.SetResource(ResourceLoggerKey, m.logger)
}
func (m *actionlogModule) Stop(_ context.Context) error            { return nil }
func (m *actionlogModule) HealthChecks() []health.DependencyCheck { return nil }

// Logger returns the action logger registered via [Module], or nil
// if no module was registered.
func Logger(infra app.Infrastructure) kitactionlog.Logger {
	v, ok := infra.Resource(ResourceLoggerKey)
	if !ok {
		return nil
	}
	l, _ := v.(kitactionlog.Logger)
	return l
}
