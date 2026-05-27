package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware/stack"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/observability/v2/slo"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// Module is a self-contained unit of infrastructure that can be registered
// with the Builder via [Builder.With]. Modules are
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

// # Optional capability interfaces
//
// Module is intentionally narrow — Name + Init + Populate + Stop +
// HealthChecks cover every adapter the kit ships. Capabilities that
// only SOME adapters care about (server TLS, health-checker handle,
// internal-handler wrapping, lifecycle attachment, middleware
// installation, leader-elector / SLO-checker / tenant-policy /
// rate-limit-declaration / HTTP-config provision) are exposed as
// SEPARATE single-method interfaces below. The Builder type-switches
// each registered module against them at Run time and threads the
// relevant data through when present.
//
// Why not a single `ModuleCapabilities` struct with optional fields?
// Two reasons:
//
//   - Adapters that don't care about a capability don't have to
//     mention it at all (vs. setting `caps.TLSReceiver = nil` on
//     every constructor). Adding a capability to the kit means adding
//     one interface here; no adapter signature changes.
//   - The Builder's type-assert chain is exhaustive at compile time
//     for the adapter author: missing method = interface unsatisfied
//     = the Builder silently skips that capability for that adapter,
//     which is the desired behaviour.
//
// The cost is type-switch hell in builder.go (12 assertions in the
// Run loop). That's been called out as "bad taste" in v2 reviews;
// the answer is "yes, intentional". A v2.x candidate is to fold the
// optional interfaces into a `ModuleFeatures` struct that adapters
// fill in alongside Populate(), which would let the Builder read
// fields directly without type assertions. That's an additive change
// (current interfaces stay; new path supersedes type-switch); it
// doesn't block v2.0.0.

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

// InternalServerConfigurator is the optional capability for adapter modules
// that need to mutate the internal-ops [*http.Server] after construction —
// for example, enabling unencrypted HTTP/2 (h2c) when the adapter layers a
// gRPC service onto the internal listener. The Builder invokes
// ConfigureInternalServer once, after the internal server is built and
// before ListenAndServe runs.
type InternalServerConfigurator interface {
	ConfigureInternalServer(srv *http.Server)
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

// MiddlewarePhase is the position of a [PhasedMiddleware] in the
// public mux's inbound chain. Higher phases run earlier (closer to
// the network); lower phases run closer to the handler.
//
// The kit assigns stable numeric IDs so:
//   - Multiple modules can target the same phase (rare, but
//     deterministic via registration order within a phase).
//   - Future modules can slot in between existing phases without
//     renumbering every consumer (use phase values like 35 to land
//     between 30 and 40).
//
// Bridge modules in app/* declare their phase via
// [PhasedMiddleware.Phase] in [MiddlewareInstaller.PublicMiddleware].
type MiddlewarePhase int

const (
	// PhaseBudget runs furthest from the network so per-tenant
	// rejections still see a fully-populated tenant context.
	PhaseBudget MiddlewarePhase = 10
	// PhaseTenant extracts the tenant ID into the request context
	// before budget enforcement runs.
	PhaseTenant MiddlewarePhase = 20
	// PhaseAuth verifies JWT / PASETO / API-key credentials and
	// stamps the user identity onto the context.
	PhaseAuth MiddlewarePhase = 30
	// PhaseSignedRequest rejects unsigned or malformed requests
	// before any of the deeper crypto / context work runs.
	PhaseSignedRequest MiddlewarePhase = 40
	// PhaseRateLimit cheap-rejects hostile clients before the
	// signed-request crypto verification runs.
	PhaseRateLimit MiddlewarePhase = 50
	// PhaseStack is reserved for the kit's default stack
	// (correlation ID, recover, security headers, request logger,
	// timeout, …). Modules SHOULD NOT use this phase.
	PhaseStack MiddlewarePhase = 60
)

// PhasedMiddleware pairs a middleware function with the phase at
// which it should run. Returned by [MiddlewareInstaller.PublicMiddleware].
type PhasedMiddleware struct {
	Phase MiddlewarePhase
	Func  func(http.Handler) http.Handler
}

// MiddlewareInstaller is the optional capability for modules that
// contribute middleware to the public mux. The Builder collects
// every PhasedMiddleware from every module implementing this
// interface, sorts them by phase descending (outermost first), and
// threads them around the user handler in stable order — services
// don't need to know which middleware runs before which.
//
// Multiple modules contributing to the same phase are applied in
// module registration order, with later-registered modules wrapping
// the earlier ones at the same phase (matching the inside-out
// composition the rest of the kit uses).
//
// Modules that do not install middleware leave this interface
// unimplemented.
type MiddlewareInstaller interface {
	PublicMiddleware() []PhasedMiddleware
}

// ElectorProvider is the public capability interface implemented
// by the leader-election bridge module (app/leader). The Builder's
// cron block looks up the elector via
// `mc.Module(leader.ModuleName).(ElectorProvider).Elector()` so
// cron can gate scheduled work to the leader replica without core
// app/v2 importing the bridge package.
type ElectorProvider interface {
	Elector() leaderelection.Elector
}

// SLOCheckerProvider is the public capability interface
// implemented by the SLO bridge module (app/slo). The Builder
// reads the *slo.Checker via this interface to wire the internal-
// ops /slo handler without importing app/slo directly.
type SLOCheckerProvider interface {
	SLOChecker() *slo.Checker
}

// TenantPolicyProvider is the public capability interface
// implemented by the tenant bridge module (app/tenant). The
// budget bridge (app/budget) looks up the tenant module at Init
// time and reads the policy via this interface to verify the
// "TenantBudget requires Required tenant" invariant without
// importing app/tenant directly.
type TenantPolicyProvider interface {
	// TenantRequired reports whether tenant is required on every
	// request (the default) vs. optionally extracted.
	TenantRequired() bool
	// TenantAllowsMissingOnSafeMethods reports whether the
	// Required policy is relaxed for GET/HEAD/OPTIONS.
	TenantAllowsMissingOnSafeMethods() bool
}

// RateLimitDeclarer is implemented by modules that count as a
// rate-limit declaration for the always-on Builder validator.
// Each module that installs ANY kind of rate limiting (IP-wide,
// per-key, custom) implements this marker so [Builder.Validate]
// can verify the service made an explicit choice — either by
// declaring a limiter, or by calling [Builder.WithoutRateLimit]
// to acknowledge the trade-off.
type RateLimitDeclarer interface {
	DeclaresRateLimit()
}

// HTTPConfigProvider is the public capability published by the
// app/http bridge module. The Builder reads the public + internal
// HTTP server configuration through this interface so app/v2
// does not import app/http directly. Returning each field
// individually (rather than a single struct of types from
// app/http) keeps the dep direction clean — every field's type
// is already a foundation-level type the Builder imports.
//
// Defaults when no app/http module is registered:
//   AllowPlaintext            = false
//   OptionalClientCerts       = false
//   AllowInternalNonLoopback  = false
//   ReloadingTLS              = nil (use static cfg.TLS snapshot)
//   TLSReloadSignals          = empty
//   DisableDefaultStack       = false
//   ServerOptions             = empty
//   StackOptions              = empty
//   CustomReadiness           = nil (use auto-built handler)
type HTTPConfigProvider interface {
	AllowPlaintext() bool
	OptionalClientCerts() bool
	AllowInternalNonLoopback() bool
	ReloadingTLSOptions() ([]netutil.FilesCertificateSourceOption, bool)
	TLSReloadSignals() []os.Signal
	DisableDefaultStack() bool
	StackOptions() []stack.Option
	ServerOptions() []httpx.ServerOption
	CustomReadiness() http.Handler
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
		panic("app: NewBaseModule module name must not be empty")
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
	// ServiceName is the Builder name (the string passed to [New]).
	// Modules that need a stable service identifier — for telemetry
	// domains, lifecycle log scopes, or vendor-SDK client names —
	// read it from here instead of duplicating it at Module
	// construction time.
	ServiceName string

	// Logger is the service-level logger. Modules should create child loggers
	// via Logger.With("module", name) for scoped output.
	Logger *slog.Logger

	// Runner is the lifecycle runner for registering background goroutines.
	Runner *lifecycle.Runner

	// Config is the service's base configuration (server ports, TLS, environment).
	Config BaseConfig

	// TLSCertSource is the hot-rotation source threaded in by
	// [Builder.ReloadingTLS]. Nil when reloading TLS is not
	// configured; modules that build their own *tls.Config (the
	// default HTTP client, gRPC dial-loops, broker adapters) MUST
	// prefer this source over [Config.TLS]'s static loaders when
	// non-nil so the whole service shares one reload cycle.
	TLSCertSource netutil.CertificateSource

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
		panic("app: Module module not found (check registration order — modules are init'd in registration order)")
	}
	return m
}

// TestModuleContext builds a ModuleContext whose moduleMap is
// pre-populated with the supplied modules. It is intended for
// per-bridge unit tests that need to exercise [Module.Init] under a
// realistic [ModuleContext.LookupModule] / [ModuleContext.Module]
// shape — e.g., app/budget's cross-module Init check against
// app/tenant.
//
// Returns an error if any registered module has an empty name or a
// duplicate name, mirroring the Builder's own pre-Init validation.
func TestModuleContext(modules ...Module) (ModuleContext, error) {
	moduleMap := make(map[string]Module, len(modules))
	for _, m := range modules {
		if m == nil {
			return ModuleContext{}, fmt.Errorf("app: TestModuleContext: nil module")
		}
		name := m.Name()
		if name == "" {
			return ModuleContext{}, fmt.Errorf("app: TestModuleContext: module with empty name")
		}
		if _, dup := moduleMap[name]; dup {
			return ModuleContext{}, fmt.Errorf("app: TestModuleContext: duplicate module name %q", name)
		}
		moduleMap[name] = m
	}
	return ModuleContext{
		ServiceName: "test",
		Logger:      slog.Default(),
		Runner:      lifecycle.NewRunner(slog.Default()),
		modules:     moduleMap,
	}, nil
}

// LookupModule returns a registered module by name or nil if no
// module with that name is registered. The returned module may
// not yet have Init'd (the moduleMap is pre-populated at startup
// with all registered modules so cross-module config lookups work
// regardless of init order). Modules that need *runtime* state of
// a peer (e.g., resources published by the peer's Init) must read
// from [Infrastructure.Resource] inside the RouterFunc instead.
//
// Use this from modules that depend on another module *optionally*
// — e.g., app/cron gates jobs on app/leader if present but runs
// unguarded otherwise — or to read a peer's configuration at Init
// time (e.g., app/budget reading app/tenant's TenantRequired()).
// The panicking [ModuleContext.Module] is the right call for hard
// deps where absence is a programmer error.
func (mc ModuleContext) LookupModule(name string) Module {
	return mc.modules[name]
}

// serverTLSOptions returns the netutil.ServerTLSOption set the
// builder will apply when constructing the public server's
// *tls.Config. FR-014 [HIGH]: the default is mTLS
// (tls.RequireAndVerifyClientCert) so the kit's "TLS env enables
// global mTLS" convention holds. [Builder.OptionalClientCertificates]
// flips this back to VerifyClientCertIfGiven for services fronted by
// an external TLS terminator.
//
// Exposed (lowercase) for the unit test that pins the contract; not
// part of the public Builder surface.
func (b *Builder) serverTLSOptions() []netutil.ServerTLSOption {
	cfg := resolveHTTPConfig(b.modules)
	if cfg.optionalClientCerts {
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
	serviceName string,
	logger *slog.Logger,
	runner *lifecycle.Runner,
	cfg BaseConfig,
	tlsSource netutil.CertificateSource,
) (func(context.Context), error) {
	// Validate name uniqueness across all modules (builtin + user-registered).
	// Builder.With only checks user-registered modules; this catches collisions
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
	// Pre-populate moduleMap with all registered modules so cross-
	// module lookups via [ModuleContext.LookupModule] work regardless
	// of init order. A module's struct fields (its config) are
	// finalized at registration time, so peers can read configuration
	// (e.g., budget reading tenant's TenantRequired()) before either
	// has Init'd.
	for _, m := range modules {
		moduleMap[m.Name()] = m
	}

	mc := ModuleContext{
		ServiceName:   serviceName,
		Logger:        logger,
		Runner:        runner,
		Config:        cfg,
		TLSCertSource: tlsSource,
		modules:       moduleMap,
	}

	for _, m := range modules {
		logger.Info("initializing module", slog.String("module", m.Name()))
		if err := initOneModule(ctx, m, mc); err != nil {
			// Close already-initialized modules in reverse order.
			closeModules(ctx, initialized, logger)
			return nil, fmt.Errorf("module init failed: %w", err)
		}
		initialized = append(initialized, m)
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
					redact.Error(err),
				)
			}
		}()
	}
}
