package app

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
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

	// noPublicServer is set by [Builder.WithoutPublicServer]: no public
	// HTTP listener, router or public middleware chain; the internal
	// health/readiness/metrics server still runs.
	noPublicServer bool

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

	// httpClientTimeout overrides the default 10s outbound client
	// timeout used by the builtin httpclient module. Zero keeps 10s.
	httpClientTimeout time.Duration

	// Modules — populated by [Builder.With]. Adapter sub-packages
	// (app/postgres, app/redis, app/amqp, app/nats, app/tracing, app/grpc,
	// app/jwt, app/paseto, app/leader, app/slo, …) all surface as modules
	// registered through this list. The only builtin assembled in
	// [Builder.buildIntegrationModules] is the httpclient module.
	modules []Module

	// Router
	routerFn RouterFunc
	// workerFn wires a worker role (WithoutPublicServer) on the infrastructure.
	workerFn func(Infrastructure)

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

// WithoutPublicServer runs the service without a public HTTP listener, for
// worker roles (queue consumers, schedulers, relays) that serve no inbound
// HTTP traffic. The internal server (readiness, health, metrics) and every
// module, background goroutine and runner-attached component (for example a
// public gRPC server from app/grpc) still run.
//
// In this mode [Builder.Router] is rejected by [Builder.Validate]; wire the
// role on the infrastructure with [Builder.Worker] instead. Module
// public middleware is not installed, and the public-listener requirements
// do not apply: no rate-limit declaration and no public TLS configuration
// are required, and BaseConfig.Server is not validated. Registering a
// rate-limit module is rejected because it would protect nothing. The
// internal-host loopback check and TLS file validation still apply.
func (b *Builder) WithoutPublicServer() *Builder {
	b.noPublicServer = true
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
// router. If fn returns a non-nil error, the entire service shuts down;
// if it returns nil, it has finished and the service keeps running.
//
// Name kept (NOT `WithBackground`) because Background is a noun in
// the kit's vocabulary — alongside `Storage`, `AuditLog`, `Router` —
// rather than an option being attached. The `With*` prefix is reserved
// for opts that toggle an existing primitive (`WithJWT`, `WithIPRateLimit`).
// Reuse via `Builder.Background(name, fn)` at the Builder seam, OR via
// `infra.Background(name, fn)` inside the RouterFunc closure for
// goroutines that need access to wired infrastructure.
func (b *Builder) Background(name string, fn func(ctx context.Context) error) *Builder {
	validateBackgroundSpec(name, fn)
	b.earlyBgs = append(b.earlyBgs, bgSpec{name: name, fn: fn})
	return b
}

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

// WithHTTPClientTimeout overrides the default 10s timeout on the kit's
// shared outbound *http.Client (app.HTTPClient). Use for slow upstreams
// (file transfer, LLM APIs) that still need the TLS hot-reload and OTel
// instrumentation the httpclient module centralizes.
func (b *Builder) WithHTTPClientTimeout(d time.Duration) *Builder {
	if d <= 0 {
		panic("app: WithHTTPClientTimeout requires a positive duration")
	}
	b.httpClientTimeout = d
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
//
// Panics if fn is nil.
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
// Init order (see [Builder.RunContext]):
//  1. User modules that implement [TracingProvider] (hoisted so the
//     builtin HTTP client can observe TracingActive at its Init)
//  2. Builtins from [Builder.buildIntegrationModules] (currently only
//     the httpclient module)
//  3. Remaining user modules in registration order
//
// Stop order is the reverse of Init. JWT, PASETO, leader election,
// cron, flags, SLO, and storage are all app/* bridge modules registered
// via With — they are not Builder builtins.
//
// Panics if module is nil, has an empty name, or has the same name
// as a module that is already registered. These are startup-time
// configuration errors.
func (b *Builder) With(m Module) *Builder {
	if m == nil {
		panic("app: With module must not be nil")
	}
	name := m.Name()
	if name == "" {
		panic("app: With module name must not be empty")
	}
	for _, existing := range b.modules {
		if existing.Name() == name {
			panic("app: With duplicate module name")
		}
	}
	b.modules = append(b.modules, m)
	return b
}

// Worker sets the function that wires a worker role ([Builder.WithoutPublicServer])
// on the infrastructure the modules populated, as [Builder.Router] does for a
// service with a public server: it runs where the RouterFunc would, after
// every module's Populate, and its [Infrastructure.Background] tasks start
// with the role. [Builder.Validate] rejects it without WithoutPublicServer.
func (b *Builder) Worker(fn func(Infrastructure)) *Builder {
	if fn == nil {
		panic("app: Worker requires a non-nil function")
	}
	b.workerFn = fn
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
		return redact.Safe("app: Builder.RunContext requires a non-nil context")
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

	r := &appRun{b: b, logger: logger, modules: b.orderedModules()}
	r.httpCfg = resolveHTTPConfig(r.modules)
	defer r.closeUnownedTLS()
	if err := r.setupServerTLS(); err != nil {
		return err
	}
	r.newRunner()
	r.addTLSReloadSignal()
	for _, bg := range b.earlyBgs {
		r.runner.AddFunc(bg.name, bg.untilShutdown())
	}

	closeModules, err := r.initModules(ctx)
	if err != nil {
		return err
	}
	defer closeModules()
	if err := r.collectModuleHealthChecks(); err != nil {
		return err
	}

	handler := r.publicHandler()
	internal := r.internalServer()
	internalErrCh := startInternalServer(ctx, internal)
	defer r.stopInternalServer(ctx, internal)
	if err := r.watchInternalServer(internalErrCh); err != nil {
		return err
	}
	r.attachAdapters()

	if b.noPublicServer {
		logger.Info("public server disabled (WithoutPublicServer)")
	} else {
		r.addPublicServer(handler)
	}
	r.closeTLS = nil
	return r.runner.Run(ctx)
}

// serverComponent serves srv on l when the HTTP module gave one, otherwise
// on srv.Addr.
func serverComponent(srv *http.Server, l net.Listener) lifecycle.Component {
	if l != nil {
		return lifecycle.NewHTTPServerOn(srv, l)
	}
	return lifecycle.NewHTTPServer(srv)
}

func listenAddr(addr string, l net.Listener) string {
	if l != nil {
		return l.Addr().String()
	}
	return addr
}
