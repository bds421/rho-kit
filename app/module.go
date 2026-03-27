package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/observability/health"
	"github.com/bds421/rho-kit/runtime/lifecycle"
)

// Module is a self-contained unit of infrastructure that can be registered
// with the Builder via [Builder.WithModule]. Modules are initialized in
// registration order during [Builder.Run], before the RouterFunc is called.
//
// Each module follows a four-phase lifecycle:
//  1. Init — connect to external services, validate config, register background
//     goroutines on the Runner. Return an error to abort startup.
//  2. Populate — expose initialized resources on the Infrastructure struct so
//     the RouterFunc can access them. Populate is called in registration order,
//     so later modules may observe fields set by earlier modules.
//  3. HealthChecks — return dependency checks for the readiness probe.
//  4. Close — release resources (connections, goroutines). Called in reverse
//     registration order during shutdown.
type Module interface {
	// Name returns a unique identifier for this module (e.g., "postgres", "redis").
	// Names must be unique across all registered modules; duplicate names panic
	// at registration time.
	Name() string

	// Init connects to external services and performs any setup work.
	// The provided ModuleContext gives access to the logger, runner, base config,
	// and previously initialized modules. Return a non-nil error to abort startup.
	Init(ctx context.Context, mc ModuleContext) error

	// Populate exposes the module's initialized resources on the Infrastructure
	// struct. This is called after all modules have been Init'd, right before
	// the RouterFunc executes. Populate is called in registration order, so
	// later modules may observe fields set by earlier modules. Modules should
	// set fields on infra that the RouterFunc needs.
	Populate(infra *Infrastructure)

	// Close releases resources held by this module. It is called in reverse
	// registration order during shutdown. The context carries a deadline.
	Close(ctx context.Context) error

	// HealthChecks returns dependency checks to add to the readiness probe.
	// Return nil or an empty slice if the module has no health checks.
	HealthChecks() []health.DependencyCheck
}

// ModuleContext provides the shared context available to modules during Init.
type ModuleContext struct {
	// Logger is the service-level logger. Modules should create child loggers
	// via Logger.With("module", name) for scoped output.
	Logger *slog.Logger

	// Runner is the lifecycle runner for registering background goroutines.
	Runner *lifecycle.Runner

	// Config is the service's base configuration (server ports, TLS, environment).
	Config BaseConfig

	// modules holds references to already-initialized modules, keyed by name.
	// This is unexported to prevent direct mutation; use the Module method.
	modules map[string]Module
}

// Module retrieves a previously initialized module by name. It panics if the
// module is not found, which indicates a registration ordering error. This is
// acceptable because it occurs at startup time, not at runtime.
func (mc ModuleContext) Module(name string) Module {
	m, ok := mc.modules[name]
	if !ok {
		panic(fmt.Sprintf("app: module %q not found (check registration order — modules are init'd in registration order)", name))
	}
	return m
}

// initModules initializes all registered modules in order and returns a
// cleanup function that closes them in reverse order. The cleanup function
// logs but does not return close errors — it is intended for use with defer.
func initModules(
	ctx context.Context,
	modules []Module,
	logger *slog.Logger,
	runner *lifecycle.Runner,
	cfg BaseConfig,
) (func(context.Context), error) {
	// Validate name uniqueness across all modules (builtin + user-registered).
	// WithModule only checks user-registered modules; this catches collisions
	// between builtin integration modules and user modules (e.g., a user module
	// named "database" when WithPostgres was also called).
	seen := make(map[string]bool, len(modules))
	for _, m := range modules {
		if seen[m.Name()] {
			return nil, fmt.Errorf("duplicate module name %q", m.Name())
		}
		seen[m.Name()] = true
	}

	initialized := make([]Module, 0, len(modules))
	moduleMap := make(map[string]Module, len(modules))

	mc := ModuleContext{
		Logger:  logger,
		Runner:  runner,
		Config:  cfg,
		modules: moduleMap,
	}

	for _, m := range modules {
		if err := initOneModule(ctx, m, mc); err != nil {
			// Close already-initialized modules in reverse order.
			closeModules(ctx, initialized, logger)
			return nil, fmt.Errorf("module %q init failed: %w", m.Name(), err)
		}
		initialized = append(initialized, m)
		moduleMap[m.Name()] = m
	}

	cleanup := func(ctx context.Context) {
		closeModules(ctx, initialized, logger)
	}

	return cleanup, nil
}

// initOneModule calls Init on a single module with panic recovery.
// A panic during Init is converted to an error so that the caller can
// still clean up already-initialized modules.
func initOneModule(ctx context.Context, m Module, mc ModuleContext) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic during init: %v", r)
		}
	}()
	return m.Init(ctx, mc)
}

// closeModules closes modules in reverse order, logging any errors.
// Each close is wrapped in panic recovery so that a misbehaving module
// cannot prevent subsequent modules from being cleaned up.
func closeModules(ctx context.Context, modules []Module, logger *slog.Logger) {
	for i := len(modules) - 1; i >= 0; i-- {
		m := modules[i]
		func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("module close panicked",
						"module", m.Name(),
						"panic", r,
					)
				}
			}()
			if err := m.Close(ctx); err != nil {
				logger.Warn("module close error",
					"module", m.Name(),
					"error", err,
				)
			}
		}()
	}
}
