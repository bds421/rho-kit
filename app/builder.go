package app

import (
	"crypto/tls"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/healthhttp"
	"github.com/bds421/rho-kit/httpx/v2/middleware/stack"
	"github.com/bds421/rho-kit/httpx/v2/slohttp"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// Builder configures and runs a service's infrastructure lifecycle.
//
// v2.0.0 lazy-adapter refactor: adapter-specific wiring (Postgres, Redis,
// RabbitMQ, NATS, OTel tracing, public gRPC) moved out of this package into
// per-adapter sub-packages so importing app/v2 no longer transitively pulls
// pgx, go-redis, amqp091, nats.go, otelgrpc, or grpc-go. Register adapters
// via [Builder.With]:
//
//	app.New("svc", "v1", base).
//	    With(postgres.Module(pgxbackend.Config{DSN: dsn})).
//	    With(redis.Module(&goredis.Options{Addr: addr})).
//	    With(amqp.Module(rabbitURL)).
//	    Router(...).
//	    Run()
//
// Lighter primitives (JWT/PASETO, signed requests, multi-tenant middleware,
// rate limiting, storage, audit log, cron, leader election, feature flags,
// SLO, action log, approval store, authz decider) stay on the Builder
// directly because their dep weight is bounded.
//
// Design note: Builder intentionally consolidates HTTP-level cross-cutting
// concerns into a single struct. This is a deliberate tradeoff:
//
//   - PRO: Services have a single, discoverable entry point with consistent
//     lifecycle management. New developers can read one file to understand
//     the infrastructure setup.
//   - PRO: Cross-cutting concerns (TLS, health checks, shutdown hooks) are
//     automatically wired — services cannot forget to register a health check
//     for a database they enabled.
//   - CON: The struct still has many fields, which violates SRP in the
//     traditional sense. However, the Builder is a composition root — its
//     job IS to compose infrastructure. Splitting into sub-builders would
//     distribute lifecycle logic across files, making the shutdown sequence
//     harder to reason about.
//
// Failure semantics: [Builder.Run] (and the panicking With*() helpers)
// must be treated as fatal. Builder does not roll back partially-acquired
// resources on failure — if one adapter module opens a pool and a later
// adapter then panics, the pool is leaked. Callers must let the process
// exit; do NOT catch the panic and retry. The Builder is a composition
// root, not a reusable factory.
type Builder struct {
	name    string
	version string
	cfg     BaseConfig
	logger  *slog.Logger

	// Rate-limit declaration opt-out. The actual IP/keyed
	// limiters live in app/ratelimit bridge modules; this field
	// only records the explicit "no rate limit" acknowledgement
	// so Builder.Validate can refuse silent un-throttled
	// deployments.
	allowNoRateLimit bool

	// Health checks
	healthChecks []health.DependencyCheck

	// customReadiness is set only by [Infrastructure.SetCustomReadiness]
	// at runtime (inside the RouterFunc). The startup-time override
	// lives on app/http.Module's CustomReadiness option. Keep the
	// field unexported so consumers don't reach for it directly.
	customReadiness http.Handler

	// Background goroutines registered before Run
	earlyBgs []bgSpec

	// Shutdown hooks
	shutdownHooks []func(context.Context)

	// startupTimeout caps how long module initialization may take
	// (FR-013). Zero means [defaultStartupTimeout]. Override via
	// [Builder.StartupTimeout] for services with genuinely slow
	// boot dependencies (e.g. Postgres warmup, KMS key fetch).
	startupTimeout time.Duration

	// Modules — populated by [Builder.With]. Adapter
	// sub-packages (app/postgres, app/redis, app/amqp, app/nats, app/tracing,
	// app/grpc) all surface as modules registered through this list, plus the
	// jwt/paseto/httpclient/leader/slo built-ins assembled in
	// [Builder.buildIntegrationModules].
	modules []Module

	// Router
	routerFn RouterFunc

	// ran guards against reuse after Run().
	ran bool
}

// New creates a Builder for the given service.
func New(name, version string, cfg BaseConfig) *Builder {
	return &Builder{
		name:    name,
		version: version,
		cfg:     cfg,
	}
}

// (MultiTenant, MultiTenantOptional, AllowMissingTenantOnSafeMethods,
// TenantBudget have moved to bridge modules — see app/tenant.Module
// and app/budget.Module respectively.)

// WithoutRateLimit acknowledges that the public HTTP server will run
// without any kit-managed rate limiter. The actual IP / keyed
// limiters live in [github.com/bds421/rho-kit/app/ratelimit/v2]
// bridge modules; calling this method records the explicit
// opt-out so [Builder.Validate] does not reject the configuration.
//
// Use this only for services with a genuine traffic bound that
// does not need application-level throttling — e.g. an internal
// cron worker reachable only through mTLS from a fixed peer set,
// an admin tool behind a VPN, or a service whose upstream
// gateway already applies a stricter limit. The check is
// unconditional — there is no KIT_ENV escape hatch. Mirrors the
// [http.WithoutTLS] shape so every always-on security tightening
// has the same affirmative declaration shape.
func (b *Builder) WithoutRateLimit() *Builder {
	b.allowNoRateLimit = true
	return b
}

// Logger sets the logger used during infrastructure setup and runtime.
// When not set, slog.Default() is used.
func (b *Builder) Logger(l *slog.Logger) *Builder {
	b.logger = l
	return b
}

func serverErrorLogOption(logger *slog.Logger) httpx.ServerOption {
	if logger == nil {
		logger = slog.Default()
	}
	return httpx.WithErrorLog(slog.NewLogLogger(logger.Handler(), slog.LevelWarn))
}

// AddHealthCheck adds a custom DependencyCheck to the readiness probe.
func (b *Builder) AddHealthCheck(check health.DependencyCheck) *Builder {
	validateDependencyCheck(check, "AddHealthCheck")
	b.healthChecks = append(b.healthChecks, check)
	return b
}

// Background registers a managed goroutine that starts before the
// router. If fn returns a non-nil error, the entire service shuts down.
//
// v2.0.0 rename: this method was previously `Builder.Background`. The
// `With*` prefix matches every other registration method on the Builder
// (`WithJWT`, `Storage`, `AuditLog`, `WithIPRateLimit`, ...) so
// IDE autocomplete on `app.New(...).With` reliably surfaces the
// background-worker primitive.
func (b *Builder) Background(name string, fn func(ctx context.Context) error) *Builder {
	validateBackgroundSpec(name, fn)
	b.earlyBgs = append(b.earlyBgs, bgSpec{name: name, fn: fn})
	return b
}

// OnShutdown registers a hook called when SIGINT/SIGTERM is received.
// Hooks run synchronously BEFORE any component's Stop is invoked, so
// DB / Redis / message-broker connections are still live when hooks
// execute. Each hook gets its own 10-second deadline; hooks that
// exceed it are abandoned without blocking the rest of the shutdown.
//
// Hooks run in registration order. Use this for "publish a final
// state" semantics: emit a goodbye message, persist last in-flight
// work, drain external producers. Closing the actual infrastructure
// is the Builder's job — don't manually close DB/Redis here.
// defaultStartupTimeout caps module initialization time so a hung
// module cannot block startup forever (FR-013). 60 seconds is a
// generous default — tighten via [Builder.StartupTimeout] for
// services with strict cold-start budgets.
const defaultStartupTimeout = 60 * time.Second

// StartupTimeout caps module initialization time. Omit this option to use
// [defaultStartupTimeout].
//
// FR-013 [MED]: pre-fix module Init ran with context.Background, so
// a module that hung during initialization (KMS DNS, broker connect)
// blocked startup forever. The deadline now triggers a typed error
// from the affected module's Init.
func (b *Builder) StartupTimeout(d time.Duration) *Builder {
	if d <= 0 {
		panic("app: StartupTimeout requires a positive duration")
	}
	b.startupTimeout = d
	return b
}

func (b *Builder) OnShutdown(fn func(context.Context)) *Builder {
	if fn == nil {
		// FR-012 [LOW]: a nil hook would otherwise panic at shutdown
		// time, well after the configuration error could be surfaced.
		// Fail fast at registration so the wiring bug is caught at
		// startup.
		panic("app: OnShutdown requires a non-nil hook function")
	}
	b.shutdownHooks = append(b.shutdownHooks, fn)
	return b
}

// With registers an adapter module returned by a sub-package's
// Module constructor (e.g., postgres.Module(...), redis.Module(...),
// amqp.Module(...), nats.Module(...), tracing.Module(...),
// grpc.Module(...), jwt.Module(...), ratelimit.IP(...), etc.).
// Using sub-package modules keeps app/v2 free of pgx, go-redis,
// amqp091, nats.go, otelgrpc, grpc-go, openfeature, and go-paseto
// imports for services that do not need them.
//
// Modules are initialized in registration order, after all built-in
// infrastructure (JWT, HTTP client, etc.) but before the RouterFunc
// is called.
//
// Panics if module is nil, has an empty name, or has the same name
// as a module that is already registered. These are startup-time
// configuration errors.
func (b *Builder) With(m Module) *Builder {
	if m == nil {
		panic("app: With: module must not be nil")
	}
	name := m.Name()
	if name == "" {
		panic("app: With: module name must not be empty")
	}
	for _, existing := range b.modules {
		if existing.Name() == name {
			panic("app: With: duplicate module name")
		}
	}
	b.modules = append(b.modules, m)
	return b
}

// Router sets the function that builds the HTTP handler from infrastructure.
func (b *Builder) Router(fn RouterFunc) *Builder {
	if fn == nil {
		panic("app: Router requires a non-nil RouterFunc")
	}
	b.routerFn = fn
	return b
}

// Run executes the full service lifecycle: init infrastructure, start workers,
// serve HTTP, wait for shutdown signal, drain workers, close connections.
//
// All long-running goroutines are managed via lifecycle.Runner. Resource cleanup
// (adapters, tracing) uses defers. The internal health server is started outside
// the Runner so it outlives workers during drain.
func (b *Builder) Run() error {
	return b.RunContext(context.Background())
}

// RunContext executes the full service lifecycle like [Builder.Run], using ctx
// as the parent context for startup and shutdown. Cancelling ctx initiates the
// same graceful drain as SIGINT/SIGTERM, which lets tests, CLIs, supervisors,
// and embedded services control the app without process-global signals.
func (b *Builder) RunContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("app: Builder.RunContext requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.ran {
		panic("app: Builder.Run() must not be called more than once")
	}
	b.ran = true

	logger := b.logger
	if logger == nil {
		logger = slog.Default()
	}

	if err := b.Validate(); err != nil {
		return err
	}

	// 0. Convert builder config to modules. Modules created from With*()
	// config are prepended so they initialize before user-registered modules.
	// Exception: an app/tracing module (any module implementing
	// [TracingProvider]) MUST initialize before the builtin HTTP-client
	// module so the HTTP client can observe TracingActive() and wrap its
	// transport in OTel instrumentation. Without this reordering the HTTP
	// client always saw tracingActive=false because the tracing module
	// hadn't initialized yet (regression-tested by
	// TestHTTPClient_PicksUpTracingFromUserModule).
	builtinModules := b.buildIntegrationModules()
	allModules := make([]Module, 0, len(builtinModules)+len(b.modules))
	var deferredUserModules []Module
	for _, m := range b.modules {
		if _, ok := m.(TracingProvider); ok {
			allModules = append(allModules, m)
		} else {
			deferredUserModules = append(deferredUserModules, m)
		}
	}
	allModules = append(allModules, builtinModules...)
	// Resolve HTTP-server config from the first registered
	// [HTTPConfigProvider] (typically app/http.Module). Zero value
	// matches the kit's hardened defaults: TLS required, default
	// stack on, internal ops loopback-only.
	httpCfg := resolveHTTPConfig(append(allModules, deferredUserModules...))
	allModules = append(allModules, deferredUserModules...)

	// 1. TLS -- server TLS is still needed here for the public server.
	// Client TLS is now handled by the httpClientModule.
	//
	// When ReloadingTLS was called, swap the static snapshot for a
	// reloading source so the public server, the default outbound HTTP
	// client, and any consumer that reads infra.TLSCertificateSource
	// share the same poll loop. The source's Close hook tears down the
	// background poller on shutdown.
	var (
		serverTLS    *tls.Config
		tlsSource    netutil.CertificateSource
		tlsReloadSrc *netutil.FilesCertificateSource
		tlsErr       error
	)
	if httpCfg.reloadingTLSActive {
		src, srcErr := b.cfg.TLS.Reloading(httpCfg.reloadingTLSOpts...)
		if srcErr != nil {
			return fmt.Errorf("build reloading TLS source: %w", srcErr)
		}
		// Thread the same server TLS options into the reloading path so
		// OptionalClientCertificates / future client-auth opt-outs
		// take effect identically whether or not TLS reload is wired —
		// otherwise enabling reload would silently flip
		// VerifyClientCertIfGiven back to RequireAndVerifyClientCert.
		serverTLS = netutil.ReloadingServerTLS(src, b.serverTLSOptions()...)
		tlsSource = src
		tlsReloadSrc = src
		b.shutdownHooks = append(b.shutdownHooks, func(context.Context) {
			if err := src.Close(); err != nil {
				logger.Warn("tls-reload source close failed", "error", err)
			}
		})
	} else {
		serverTLS, tlsErr = b.cfg.TLS.ServerTLS(b.serverTLSOptions()...)
		if tlsErr != nil {
			return fmt.Errorf("build server TLS config: %w", tlsErr)
		}
	}

	// 2. Lifecycle Runner -- manages all long-running goroutines.
	// Shutdown hooks fire from BeforeStop — synchronously after ctx
	// cancellation but BEFORE any component's Stop runs, so hooks see
	// live DB / broker / cache connections.
	runnerOpts := []lifecycle.RunnerOption{
		lifecycle.WithBeforeStop(func(ctx context.Context) {
			runShutdownHooks(ctx, b.shutdownHooks, logger)
		}),
	}
	runner := lifecycle.NewRunner(logger, runnerOpts...)

	// (EventBus is now an opt-in bridge module under app/eventbus —
	// see eventbus.Module and eventbus.Bus(infra). Services that
	// need it call b.With(eventbus.Module()).)

	// 2.6. TLS reload-on-signal bridge — only registered when the
	// caller wired TLSReloadOnSignal alongside ReloadingTLS
	// (Validate enforces the combination). The bridge runs as a
	// lifecycle component so its goroutine exits cleanly on shutdown
	// even when the FilesCertificateSource itself is closed later
	// from a shutdown hook.
	if len(httpCfg.tlsReloadSignals) > 0 && tlsReloadSrc != nil {
		signals := append([]os.Signal(nil), httpCfg.tlsReloadSignals...)
		src := tlsReloadSrc
		runner.AddFunc("tls-reload-signal", func(ctx context.Context) error {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, signals...)
			defer signal.Stop(sigCh)
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-sigCh:
					if err := src.Reload(); err != nil {
						logger.Warn("tls-reload-on-signal reload failed, keeping previous snapshot",
							"error", err)
					}
				}
			}
		})
	}

	// (Rate limiters are now opt-in bridge modules under
	// app/ratelimit — see ratelimit.IP and ratelimit.Keyed.)

	// (Audit log is now an opt-in bridge module under app/auditlog —
	// see auditlog.Module and auditlog.Logger(infra).)

	// (Cron is now an opt-in bridge module under app/cron, which
	// reads the optional leader gate via the ElectorProvider
	// capability at its Init time.)

	// 7. Early background goroutines
	for _, bg := range b.earlyBgs {
		runner.AddFunc(bg.name, bg.fn)
	}

	// 8. Modules — initialize in registration order, close in reverse on shutdown.
	//
	// Pre-Init hook: adapter modules that need the kit-level serverTLS
	// (e.g., app/grpc) implement ServerTLSReceiver and are handed the
	// resolved *tls.Config before Init runs. This lets them auto-wire
	// the same TLS surface as the HTTP server without app/v2 importing
	// the adapter packages.
	if serverTLS != nil {
		for _, m := range allModules {
			if recv, ok := m.(ServerTLSReceiver); ok {
				recv.SetServerTLS(serverTLS)
			}
		}
	}
	if len(allModules) > 0 {
		// FR-013 [MED]: bound module Init so a module that hangs
		// (KMS DNS, broker connect) cannot block startup forever.
		startupTimeout := b.startupTimeout
		if startupTimeout <= 0 {
			startupTimeout = defaultStartupTimeout
		}
		initCtx, initCancel := context.WithTimeout(ctx, startupTimeout)
		moduleCleanup, moduleErr := initModules(
			initCtx,
			allModules,
			b.name,
			logger,
			runner,
			b.cfg,
			tlsSource,
		)
		initCancel()
		if moduleErr != nil {
			return moduleErr
		}
		defer func() {
			cleanupCtx, cancel := detachedTimeoutContext(ctx, 10*time.Second)
			defer cancel()
			moduleCleanup(cleanupCtx)
		}()

		// Collect health checks from all modules.
		for _, m := range allModules {
			checks := m.HealthChecks()
			for _, check := range checks {
				if err := health.ValidateDependencyCheck(check); err != nil {
					return fmt.Errorf("module health check invalid: %w", err)
				}
			}
			b.healthChecks = append(b.healthChecks, checks...)
		}
	}

	// 10. Router — build HTTP handler with all infrastructure available.
	// lateBgs collects goroutines registered via infra.Background() inside routerFn.
	// A mutex protects concurrent access, and a frozen flag prevents calls after
	// routerFn returns (the goroutines would be lost).
	var lateBgs []bgSpec
	var lateBgsMu sync.Mutex
	var lateBgsFrozen bool

	infra := Infrastructure{
		Logger:        logger,
		ServerTLS:     serverTLS,
		TLSCertSource: tlsSource,
		Config:        b.cfg,
		Background: func(name string, fn func(ctx context.Context) error) {
			validateBackgroundSpec(name, fn)
			lateBgsMu.Lock()
			defer lateBgsMu.Unlock()
			if lateBgsFrozen {
				panic("app: Background() must only be called synchronously within RouterFunc")
			}
			lateBgs = append(lateBgs, bgSpec{name: name, fn: fn})
		},
		SetCustomReadiness: func(h http.Handler) {
			if h == nil {
				panic("app: SetCustomReadiness requires a non-nil handler")
			}
			lateBgsMu.Lock()
			defer lateBgsMu.Unlock()
			if lateBgsFrozen {
				panic("app: SetCustomReadiness() must only be called synchronously within RouterFunc")
			}
			b.customReadiness = h
		},
		AddHealthCheck: func(check health.DependencyCheck) {
			validateDependencyCheck(check, "Infrastructure.AddHealthCheck")
			lateBgsMu.Lock()
			defer lateBgsMu.Unlock()
			if lateBgsFrozen {
				panic("app: AddHealthCheck() must only be called synchronously within RouterFunc")
			}
			b.healthChecks = append(b.healthChecks, check)
		},
	}

	// Let modules populate the infrastructure before the RouterFunc sees it.
	for _, m := range allModules {
		m.Populate(&infra)
	}

	var httpHandler http.Handler
	if b.routerFn != nil {
		httpHandler = b.routerFn(infra)
	} else {
		httpHandler = http.NotFoundHandler()
	}
	// Compose the inbound middleware chain. Bridge modules in app/*
	// declare which phase they target via [MiddlewareInstaller];
	// the kit's hardcoded ordering (budget → tenant → auth →
	// signedrequest → ratelimit → stack) lives in the
	// [MiddlewarePhase] constants, not here. This block stays the
	// canonical assembly point so adapters cannot independently
	// inject middleware in arbitrary positions — the Builder
	// remains the single owner of chain order.
	//
	// Tenant + budget are now MiddlewareInstaller modules; their
	// PhaseTenant / PhaseBudget contributions flow through
	// applyPhasedMiddleware alongside every other bridge.
	httpHandler = applyPhasedMiddleware(httpHandler, allModules)
	if !httpCfg.disableDefaultStack {
		// FR-009 [MED]: pass the resolved logger so the request stack uses
		// the same logger as infrastructure setup. Pre-fix this hardcoded
		// slog.Default(), so [Builder.Logger] silently failed to take
		// effect on the public middleware chain.
		httpHandler = stack.Default(httpHandler, logger, httpCfg.stackOpts...)
	}

	// Freeze late background registration — any calls after routerFn returns
	// would silently lose the goroutine since we've already iterated lateBgs.
	lateBgsMu.Lock()
	lateBgsFrozen = true
	lateBgsMu.Unlock()

	// Start late background goroutines (registered inside routerFn).
	for _, bg := range lateBgs {
		runner.AddFunc(bg.name, bg.fn)
	}

	// 11. Internal server (readiness + health + metrics).
	// Started outside the Runner so it outlives workers during drain —
	// health checks and metrics remain available while components shut down.
	healthChecker := &health.Checker{
		Version: health.ResolveVersion(b.version),
		Checks:  b.healthChecks,
	}

	// Adapter modules that participate in health-checker wiring (e.g., the
	// public gRPC server's RegisterHealth path used by app/grpc) implement
	// HealthCheckerReceiver. The Builder hands them the resolved checker
	// before assembling internal handlers so the adapter can register
	// the gRPC health service on its own grpc.Server when configured.
	for _, m := range allModules {
		if recv, ok := m.(HealthCheckerReceiver); ok {
			recv.SetHealthChecker(healthChecker)
		}
	}

	// Runtime SetCustomReadiness (set inside RouterFunc) wins over
	// the startup-time override from app/http.Module(http.CustomReadiness(...)).
	var readiness http.Handler
	switch {
	case b.customReadiness != nil:
		readiness = b.customReadiness
	case httpCfg.customReadiness != nil:
		readiness = httpCfg.customReadiness
	default:
		readiness = healthhttp.Handler(healthChecker)
	}
	var internalOpts []healthhttp.InternalHandlerOption
	for _, m := range allModules {
		if sp, ok := m.(SLOCheckerProvider); ok {
			if checker := sp.SLOChecker(); checker != nil {
				internalOpts = append(internalOpts, healthhttp.WithSLOHandler(slohttp.Handler(checker)))
				break
			}
		}
	}
	serverErrorLogOpt := serverErrorLogOption(logger)
	internalHandler := healthhttp.NewInternalHandler(b.version, readiness, internalOpts...)
	// Adapter modules can wrap the internal handler to add transport-specific
	// endpoints (e.g., gRPC health over h2c when app/grpc is registered).
	// This avoids importing grpc-go from app/v2 just to serve internal health.
	for _, m := range allModules {
		if wrap, ok := m.(InternalHandlerWrapper); ok {
			internalHandler = wrap.WrapInternalHandler(internalHandler, healthChecker)
		}
	}
	internalSrv := httpx.NewServer(b.cfg.Internal.Addr(), internalHandler, serverErrorLogOpt)
	// Adapter modules may mutate the internal server post-construction
	// (e.g., app/grpc enables UnencryptedHTTP2 so the gRPC health
	// service rides the same listener without the deprecated
	// h2c.NewHandler wrapper).
	for _, m := range allModules {
		if cfg, ok := m.(InternalServerConfigurator); ok {
			cfg.ConfigureInternalServer(internalSrv)
		}
	}
	internalErrCh := make(chan error, 1)
	go func() {
		if srvErr := internalSrv.ListenAndServe(); srvErr != nil && !errors.Is(srvErr, http.ErrServerClosed) {
			internalErrCh <- srvErr
		}
	}()
	defer func() {
		shutdownCtx, cancel := detachedTimeoutContext(ctx, 5*time.Second)
		defer cancel()
		if shutdownErr := internalSrv.Shutdown(shutdownCtx); shutdownErr != nil {
			logger.Warn("internal server shutdown error", redact.Error(shutdownErr))
		}
	}()

	// Brief pause to detect early bind failures (port conflict, permission denied).
	select {
	case srvErr := <-internalErrCh:
		return fmt.Errorf("internal server failed to start: %w", srvErr)
	case <-time.After(50 * time.Millisecond):
	}
	logger.Info("internal server started", slog.String("addr", b.cfg.Internal.Addr()))

	// Monitor internal server for post-startup failures (e.g., accept loop errors).
	// If the internal server dies, the entire service shuts down — running without
	// health checks or metrics is unsafe in production.
	runner.AddFunc("internal-server-monitor", func(ctx context.Context) error {
		select {
		case srvErr := <-internalErrCh:
			return fmt.Errorf("internal server failed: %w", srvErr)
		case <-ctx.Done():
			return nil
		}
	})

	// 13. Adapter-owned long-running components (e.g., the public gRPC
	// server in app/grpc) implement [lifecycle.Component] and register
	// themselves with the Runner via [Module.AttachToRunner]. Calling
	// AttachToRunner here — before public-server registration — means
	// the gRPC server is stopped AFTER the public HTTP server (reverse
	// registration order).
	for _, m := range allModules {
		if attacher, ok := m.(RunnerAttacher); ok {
			attacher.AttachToRunner(runner)
		}
	}

	// 14. Public server — added last so it is stopped first (reverse order).
	srvOpts := make([]httpx.ServerOption, 0, len(httpCfg.serverOpts)+2)
	srvOpts = append(srvOpts, serverErrorLogOpt)
	if serverTLS != nil {
		srvOpts = append(srvOpts, httpx.WithTLSConfig(serverTLS))
	}
	srvOpts = append(srvOpts, httpCfg.serverOpts...)
	srv := httpx.NewServer(b.cfg.Server.Addr(), httpHandler, srvOpts...)
	runner.Add("public-server", lifecycle.HTTPServer(srv))

	// 15. Run — signal handling, component lifecycle, graceful shutdown.
	return runner.Run(ctx)
}
