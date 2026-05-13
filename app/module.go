package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// Module is a self-contained unit of infrastructure that can be registered
// with the Builder via [Builder.With] or [Builder.WithModule]. Modules are
// initialized in registration order during [Builder.Run], before the
// RouterFunc is called.
//
// Each module follows a four-phase lifecycle:
//  1. Init — connect to external services, validate config, register background
//     goroutines on the Runner. Return an error to abort startup.
//  2. Populate — expose initialized resources on the Infrastructure struct so
//     the RouterFunc can access them. Populate is called in registration order,
//     so later modules may observe fields set by earlier modules.
//  3. HealthChecks — return dependency checks for the readiness probe.
//  4. Stop — release resources (connections, goroutines). Called in reverse
//     registration order during shutdown. Stop's signature matches
//     [lifecycle.Component.Stop] so modules can also be registered with the
//     Runner directly.
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

	// Stop releases resources held by this module. It is called in reverse
	// registration order during shutdown. The context carries a deadline.
	// Stop must be idempotent — modules also registered as
	// [lifecycle.Component] may have Stop invoked by the Runner first.
	Stop(ctx context.Context) error

	// HealthChecks returns dependency checks to add to the readiness probe.
	// Return nil or an empty slice if the module has no health checks.
	HealthChecks() []health.DependencyCheck
}

// ServerTLSReceiver is the optional capability that adapter modules implement
// when they need the Builder's resolved server-side *tls.Config (mirroring the
// public HTTP listener's TLS surface). [Builder.Run] hands the resolved config
// to every module implementing this interface before [Module.Init] runs, so
// the adapter can wire matching credentials onto its own listener
// (e.g., grpc.Creds for the public gRPC server in app/grpc).
//
// Modules without TLS-aware listeners leave this interface unimplemented and
// the Builder ignores them.
type ServerTLSReceiver interface {
	SetServerTLS(cfg *tls.Config)
}

// HealthCheckerReceiver is the optional capability for adapter modules that
// need a reference to the Builder's [health.Checker] (e.g., app/grpc registers
// the gRPC health-checking service on its grpc.Server using the same checker
// that powers the HTTP readiness probe). The Builder calls SetHealthChecker
// after collecting every module's health checks and before starting the
// public/internal listeners.
type HealthCheckerReceiver interface {
	SetHealthChecker(checker *health.Checker)
}

// InternalHandlerWrapper is the optional capability for adapter modules that
// wrap the kit's internal-ops HTTP handler to add a transport-specific
// endpoint (e.g., app/grpc layers the gRPC health-checking service over h2c
// on the same internal listener so internal callers can probe via either
// protocol). The Builder invokes WrapInternalHandler exactly once, after the
// HTTP-level internal handler is assembled.
type InternalHandlerWrapper interface {
	WrapInternalHandler(base http.Handler, checker *health.Checker) http.Handler
}

// RunnerAttacher is the optional capability for adapter modules that own a
// long-running component which must be registered with the lifecycle Runner
// (e.g., the public gRPC server's Start/Stop loop in app/grpc). The Builder
// calls AttachToRunner after module Init succeeds and before the public HTTP
// server is registered, so reverse-order shutdown drains the HTTP server
// first.
type RunnerAttacher interface {
	AttachToRunner(runner *lifecycle.Runner)
}

// BaseModule provides no-op defaults for optional Module methods. Embed it in
// custom module structs to avoid implementing methods you don't need:
//
//	type MyModule struct {
//	    app.BaseModule
//	    conn *db.Conn
//	}
//
//	func NewMyModule() *MyModule {
//	    return &MyModule{BaseModule: app.NewBaseModule("my-module")}
//	}
//
//	func (m *MyModule) Init(ctx context.Context, mc app.ModuleContext) error { ... }
//	func (m *MyModule) Stop(ctx context.Context) error { return m.conn.Close() }
//
// Only Name is required at construction; Init, Populate, Stop, and HealthChecks
// all have safe no-op defaults.
type BaseModule struct {
	name string
}

// NewBaseModule creates a BaseModule with the given name.
// Panics if name is empty.
func NewBaseModule(name string) BaseModule {
	if name == "" {
		panic("app: module name must not be empty")
	}
	return BaseModule{name: name}
}

func (b BaseModule) Name() string                                  { return b.name }
func (b BaseModule) Init(_ context.Context, _ ModuleContext) error { return nil }
func (b BaseModule) Populate(_ *Infrastructure)                    {}
func (b BaseModule) Stop(_ context.Context) error                  { return nil }
func (b BaseModule) HealthChecks() []health.DependencyCheck        { return nil }

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
		panic("app: module not found (check registration order — modules are init'd in registration order)")
	}
	return m
}

// serverTLSOptions returns the netutil.ServerTLSOption set the
// builder will apply when constructing the public server's
// *tls.Config. FR-014 [HIGH]: the default is mTLS
// (tls.RequireAndVerifyClientCert) so the kit's "TLS env enables
// global mTLS" convention holds. [Builder.WithOptionalClientCertificates]
// flips this back to VerifyClientCertIfGiven for services fronted by
// an external TLS terminator.
//
// Exposed (lowercase) for the unit test that pins the contract; not
// part of the public Builder surface.
func (b *Builder) serverTLSOptions() []netutil.ServerTLSOption {
	if b.tlsOptionalClientCert {
		return []netutil.ServerTLSOption{netutil.WithOptionalClientCert()}
	}
	return nil
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
	// named "database" when a postgres adapter module was also registered).
	seen := make(map[string]bool, len(modules))
	for _, m := range modules {
		if seen[m.Name()] {
			panic("app: duplicate module name (builtin + user modules must have unique names)")
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
		logger.Info("initializing module", slog.String("module", m.Name()))
		if err := initOneModule(ctx, m, mc); err != nil {
			// Close already-initialized modules in reverse order.
			closeModules(ctx, initialized, logger)
			return nil, fmt.Errorf("module init failed: %w", err)
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
			err = fmt.Errorf("panic during init: %s", redact.PanicValue(r))
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
					logger.Error("module stop panicked",
						slog.String("module", m.Name()),
						redact.Panic(r),
					)
				}
			}()
			logger.Info("stopping module", slog.String("module", m.Name()))
			if err := m.Stop(ctx); err != nil {
				logger.Warn("module stop error",
					slog.String("module", m.Name()),
					slog.Any("error", err),
				)
			}
		}()
	}
}
