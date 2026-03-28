package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/httpx"
	"github.com/bds421/rho-kit/httpx/healthhttp"
	mwrl "github.com/bds421/rho-kit/httpx/middleware/ratelimit"
	"github.com/bds421/rho-kit/httpx/slohttp"
	kitredis "github.com/bds421/rho-kit/infra/redis"
	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormmysql"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
	"github.com/bds421/rho-kit/infra/storage"
	"github.com/bds421/rho-kit/observability/auditlog"
	"github.com/bds421/rho-kit/observability/health"
	"github.com/bds421/rho-kit/observability/slo"
	"github.com/bds421/rho-kit/observability/tracing"
	kitcron "github.com/bds421/rho-kit/runtime/cron"
	"github.com/bds421/rho-kit/runtime/eventbus"
	"github.com/bds421/rho-kit/runtime/lifecycle"
)

// Builder configures and runs a service's infrastructure lifecycle.
//
// Import cost: The app package imports database, redis, messaging, and storage
// modules. Services that only need HTTP+Redis still pull in GORM and AMQP as
// transitive dependencies. This is acceptable for monorepo services that
// typically use most infrastructure. For lightweight services (e.g., a pure
// HTTP proxy), use the individual packages directly with lifecycle.Runner
// instead of the Builder — the kit's sub-packages have no upward dependency
// on app and can be composed independently.
//
// Design note: Builder intentionally consolidates all infrastructure concerns
// (DB, Redis, MQ, JWT, storage, tracing, health, rate limiting) into a single
// struct. This is a deliberate tradeoff:
//
//   - PRO: Services have a single, discoverable entry point with consistent
//     lifecycle management. New developers can read one file to understand
//     the infrastructure setup.
//   - PRO: Cross-cutting concerns (TLS, health checks, shutdown hooks) are
//     automatically wired — services cannot forget to register a health check
//     for a database they enabled.
//   - CON: The struct has many fields (20+), which violates SRP in the
//     traditional sense. However, the Builder is a composition root — its
//     job IS to compose infrastructure. Splitting into sub-builders would
//     distribute lifecycle logic across files, making the shutdown sequence
//     harder to reason about.
//
// If a service needs only a subset (e.g., Redis + HTTP without DB/MQ),
// simply omit the With*() calls — unused infrastructure has zero cost
// at runtime. The Builder only initializes what is configured.
type Builder struct {
	name    string
	version string
	cfg     BaseConfig
	logger  *slog.Logger

	// DB
	dbDriver    gormdb.Driver
	dbCfg       *sqldb.Config
	dbPoolCfg   *sqldb.PoolConfig
	dbMetrics   bool
	dbNamespace string

	// Redis
	redisOpts     *goredis.Options
	redisConnOpts []kitredis.ConnOption

	// RabbitMQ
	mqURL          string
	criticalBroker bool

	// JWT
	jwksURL string

	// Rate limiters
	ipRateRequests int
	ipRateWindow   time.Duration
	keyedLimiters  []keyedLimiterSpec

	// Storage
	storageBackend storage.Storage
	storageSpecs   []storageSpec

	// Audit log
	auditStore auditlog.Store
	auditOpts  []auditlog.Option

	// EventBus worker pool
	eventBusPoolSize int

	// Cron
	cronOpts    []kitcron.Option
	cronEnabled bool

	// Migrations
	migrationsDir fs.FS

	// Seed
	seedFn SeedFunc

	// Tracing
	tracingCfg *tracing.Config

	// Server options
	serverOpts []httpx.ServerOption

	// Health checks
	healthChecks    []health.DependencyCheck
	customReadiness http.Handler

	// Background goroutines registered before Run
	earlyBgs []bgSpec

	// Shutdown hooks
	shutdownHooks []func(context.Context)

	// Modules
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

// WithMySQL configures a MariaDB connection.
// Panics if WithPostgres was already called — the two are mutually exclusive.
// Use [WithMigrations] to apply goose schema migrations.
func (b *Builder) WithMySQL(cfg sqldb.Config, pool sqldb.PoolConfig) *Builder {
	if b.dbDriver != nil {
		panic("app.Builder: WithMySQL and WithPostgres are mutually exclusive")
	}
	b.dbDriver = gormmysql.MySQLDriver{}
	b.dbCfg = &cfg
	b.dbPoolCfg = &pool
	b.dbNamespace = b.name
	return b
}

// WithPostgres configures a PostgreSQL connection.
// Panics if WithMySQL was already called — the two are mutually exclusive.
// Use [WithMigrations] to apply goose schema migrations.
func (b *Builder) WithPostgres(cfg sqldb.Config, pool sqldb.PoolConfig) *Builder {
	if b.dbDriver != nil {
		panic("app.Builder: WithPostgres and WithMySQL are mutually exclusive")
	}
	b.dbDriver = gormpostgres.PostgresDriver{}
	b.dbCfg = &cfg
	b.dbPoolCfg = &pool
	b.dbNamespace = b.name
	return b
}

// WithDBMetrics enables Prometheus pool metrics exported every 15s.
func (b *Builder) WithDBMetrics() *Builder {
	b.dbMetrics = true
	return b
}

// WithRedis configures a Redis connection with health checks and pool metrics.
// The connection uses lazy connect by default so the service can start accepting
// requests while Redis is still connecting. The connection is available via
// infra.Redis in the RouterFunc.
//
// Additional ConnOption values are appended after the builder's defaults
// (WithLogger, WithLazyConnect). Pass WithInstance to label metrics when
// multiple Redis connections exist.
func (b *Builder) WithRedis(opts *goredis.Options, connOpts ...kitredis.ConnOption) *Builder {
	if opts == nil {
		panic("app: redis options must not be nil")
	}
	b.redisOpts = opts
	b.redisConnOpts = connOpts
	return b
}

// WithRabbitMQ configures a RabbitMQ connection with lazy connect.
// The broker health check defaults to non-critical (degraded, not 503).
// Panics if url is empty — use environment variables to conditionally skip.
func (b *Builder) WithRabbitMQ(url string) *Builder {
	if url == "" {
		panic("app: WithRabbitMQ requires a non-empty URL")
	}
	b.mqURL = url
	return b
}

// WithCriticalBroker makes the RabbitMQ health check critical (503 on failure).
func (b *Builder) WithCriticalBroker() *Builder {
	b.criticalBroker = true
	return b
}

// WithJWT configures a JWKS provider for JWT verification.
// Panics if jwksURL is empty — use environment variables to conditionally skip.
func (b *Builder) WithJWT(jwksURL string) *Builder {
	if jwksURL == "" {
		panic("app: WithJWT requires a non-empty JWKS URL")
	}
	b.jwksURL = jwksURL
	return b
}

// WithIPRateLimit configures a per-IP rate limiter.
func (b *Builder) WithIPRateLimit(requests int, window time.Duration) *Builder {
	b.ipRateRequests = requests
	b.ipRateWindow = window
	return b
}

// WithKeyedRateLimit adds a named keyed rate limiter. The limiter is accessible
// via Infrastructure.KeyedLimiters[name].
func (b *Builder) WithKeyedRateLimit(name string, requests int, window time.Duration) *Builder {
	for _, existing := range b.keyedLimiters {
		if existing.name == name {
			panic(fmt.Sprintf("app: duplicate keyed rate limiter name %q", name))
		}
	}
	b.keyedLimiters = append(b.keyedLimiters, keyedLimiterSpec{
		name:     name,
		requests: requests,
		window:   window,
	})
	return b
}

// WithStorage registers an object storage backend and optional health checks.
// The backend is available via infra.Storage in the RouterFunc.
// Panics if backend is nil — this catches misconfiguration at startup.
func (b *Builder) WithStorage(backend storage.Storage, checks ...health.DependencyCheck) *Builder {
	if backend == nil {
		panic("app: storage backend must not be nil")
	}
	b.storageBackend = backend
	b.healthChecks = append(b.healthChecks, checks...)
	return b
}

// WithNamedStorage registers a named storage backend. All named backends are
// accessible via infra.StorageManager.Disk(name) in the RouterFunc. The first
// registered backend becomes the default disk unless [SetDefault] is called on
// the manager. Health checks are added to the readiness probe.
//
// This can be used alongside [WithStorage] — the unnamed backend is independent
// of the manager.
func (b *Builder) WithNamedStorage(name string, backend storage.Storage, checks ...health.DependencyCheck) *Builder {
	if name == "" {
		panic("app: storage name must not be empty")
	}
	if backend == nil {
		panic("app: storage backend must not be nil")
	}
	b.storageSpecs = append(b.storageSpecs, storageSpec{
		name:    name,
		backend: backend,
	})
	b.healthChecks = append(b.healthChecks, checks...)
	return b
}

// WithAuditLog configures an audit log backed by the given store. The audit
// logger is available via infra.AuditLog in the RouterFunc. Use it to log
// domain events or attach the audit middleware to the HTTP handler.
func (b *Builder) WithAuditLog(store auditlog.Store, opts ...auditlog.Option) *Builder {
	if store == nil {
		panic("app: audit log store must not be nil")
	}
	b.auditStore = store
	b.auditOpts = opts
	return b
}

// WithCron enables the cron scheduler. The scheduler is available via
// infra.Cron in the RouterFunc, where jobs can be added with infra.Cron.Add().
// The scheduler is started as a lifecycle component before the HTTP server
// and stopped during graceful shutdown (waits for running jobs to complete).
func (b *Builder) WithCron(opts ...kitcron.Option) *Builder {
	b.cronEnabled = true
	b.cronOpts = opts
	return b
}

// WithEventBusPool configures a bounded worker pool for the in-process event
// bus. Without this, async event handlers launch unbounded goroutines.
// The pool is registered on the lifecycle runner so it starts and stops
// with the service.
//
// Panics if size is not positive.
func (b *Builder) WithEventBusPool(size int) *Builder {
	if size <= 0 {
		panic("app: WithEventBusPool requires a positive pool size")
	}
	b.eventBusPoolSize = size
	return b
}

// WithMigrations configures goose SQL migrations. Requires WithMySQL or
// WithPostgres — panics at Run() if neither is configured.
//
// Goose migrations always run regardless of environment. This ensures
// dev, staging, and production use the same schema migration path.
func (b *Builder) WithMigrations(dir fs.FS) *Builder {
	if dir == nil {
		panic("app: WithMigrations requires a non-nil fs.FS")
	}
	b.migrationsDir = dir
	return b
}

// WithSeed enables the --seed flag for database seeding.
func (b *Builder) WithSeed(fn SeedFunc) *Builder {
	b.seedFn = fn
	return b
}

// WithTracing enables OpenTelemetry distributed tracing. If the config's
// Endpoint is empty, a noop provider is used (zero overhead).
func (b *Builder) WithTracing(cfg tracing.Config) *Builder {
	b.tracingCfg = &cfg
	return b
}

// WithLogger sets the logger used during infrastructure setup and runtime.
// When not set, slog.Default() is used.
func (b *Builder) WithLogger(l *slog.Logger) *Builder {
	b.logger = l
	return b
}

// WithServerOption adds an httpx.ServerOption to the public HTTP server.
func (b *Builder) WithServerOption(opt httpx.ServerOption) *Builder {
	b.serverOpts = append(b.serverOpts, opt)
	return b
}

// AddHealthCheck adds a custom DependencyCheck to the readiness probe.
func (b *Builder) AddHealthCheck(check health.DependencyCheck) *Builder {
	b.healthChecks = append(b.healthChecks, check)
	return b
}

// WithCustomReadiness overrides the auto-accumulated health checks with a
// custom readiness handler (e.g. for custom state introspection).
func (b *Builder) WithCustomReadiness(h http.Handler) *Builder {
	b.customReadiness = h
	return b
}

// Background registers a managed goroutine that starts before the router.
// If fn returns a non-nil error, the entire service shuts down.
func (b *Builder) Background(name string, fn func(ctx context.Context) error) *Builder {
	b.earlyBgs = append(b.earlyBgs, bgSpec{name: name, fn: fn})
	return b
}

// OnShutdown registers a hook called when SIGINT/SIGTERM is received,
// before workers are drained. The context carries a per-hook deadline
// (10 seconds by default) so hooks can respect shutdown timeouts.
func (b *Builder) OnShutdown(fn func(context.Context)) *Builder {
	b.shutdownHooks = append(b.shutdownHooks, fn)
	return b
}

// WithModule registers a module for initialization during Run(). Modules are
// initialized in registration order, after all built-in infrastructure (DB,
// Redis, MQ, etc.) but before the RouterFunc is called.
//
// Panics if module is nil or if a module with the same name is already registered.
// This is a startup-time configuration error.
func (b *Builder) WithModule(m Module) *Builder {
	if m == nil {
		panic("app: module must not be nil")
	}
	name := m.Name()
	if name == "" {
		panic("app: module name must not be empty")
	}
	for _, existing := range b.modules {
		if existing.Name() == name {
			panic(fmt.Sprintf("app: duplicate module name %q", name))
		}
	}
	b.modules = append(b.modules, m)
	return b
}

// WithSLO enables SLO monitoring with the given definitions. Creates a checker
// backed by prometheus.DefaultGatherer, registers a non-critical health check,
// and wires a /slo JSON endpoint on the internal ops server.
func (b *Builder) WithSLO(slos ...slo.SLO) *Builder {
	return b.WithModule(newSLOModule(slos...))
}

// Router sets the function that builds the HTTP handler from infrastructure.
func (b *Builder) Router(fn RouterFunc) *Builder {
	b.routerFn = fn
	return b
}

// Run executes the full service lifecycle: init infrastructure, start workers,
// serve HTTP, wait for shutdown signal, drain workers, close connections.
//
// All long-running goroutines are managed via lifecycle.Runner. Resource cleanup
// (DB, MQ, tracing) uses defers. The internal health server is started outside
// the Runner so it outlives workers during drain.
func (b *Builder) Run() error {
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
	builtinModules, dbMod := b.buildIntegrationModules()
	allModules := make([]Module, 0, len(builtinModules)+len(b.modules))
	allModules = append(allModules, builtinModules...)
	allModules = append(allModules, b.modules...)

	// 0.5. EventBus — always initialized (no With* required).
	busOpts := []eventbus.Option{eventbus.WithLogger(logger)}
	if b.eventBusPoolSize > 0 {
		busOpts = append(busOpts, eventbus.WithWorkerPool(b.eventBusPoolSize))
	}
	eventBus := eventbus.New(busOpts...)

	// 1. TLS -- server TLS is still needed here for the public server.
	// Client TLS is now handled by the httpClientModule.
	serverTLS, err := b.cfg.TLS.ServerTLS()
	if err != nil {
		return fmt.Errorf("build server TLS config: %w", err)
	}

	// 2. Lifecycle Runner -- manages all long-running goroutines.
	runner := lifecycle.NewRunner(logger)

	// 2.5. EventBus pool lifecycle -- register after runner creation.
	if b.eventBusPoolSize > 0 {
		runner.Add("eventbus", eventBus)
	}

	// 3. Rate limiters
	var rl *mwrl.RateLimiter
	if b.ipRateRequests > 0 {
		rl = mwrl.NewRateLimiter(b.ipRateRequests, b.ipRateWindow)
		runner.AddFunc("rate-limiter-cleanup", func(ctx context.Context) error {
			rl.Run(ctx)
			return nil
		})
	}

	keyedLimiters := make(map[string]*mwrl.KeyedRateLimiter, len(b.keyedLimiters))
	for _, spec := range b.keyedLimiters {
		kl := mwrl.NewKeyedRateLimiter(spec.requests, spec.window)
		keyedLimiters[spec.name] = kl
		name := "keyed-limiter-" + spec.name
		runner.AddFunc(name, func(ctx context.Context) error {
			kl.Run(ctx)
			return nil
		})
	}

	// 5. Audit log
	var auditLogger *auditlog.Logger
	if b.auditStore != nil {
		auditLogger = auditlog.New(b.auditStore, b.auditOpts...)
	}

	// 6. Cron scheduler
	var cronScheduler *kitcron.Scheduler
	if b.cronEnabled {
		cronScheduler = kitcron.New(logger, b.cronOpts...)
		runner.Add("cron-scheduler", cronScheduler)
	}

	// 7. Early background goroutines
	for _, bg := range b.earlyBgs {
		runner.AddFunc(bg.name, bg.fn)
	}

	// 8. Modules — initialize in registration order, close in reverse on shutdown.
	if len(allModules) > 0 {
		moduleCleanup, moduleErr := initModules(
			context.Background(),
			allModules,
			logger,
			runner,
			b.cfg,
		)
		if moduleErr != nil {
			return moduleErr
		}
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			moduleCleanup(cleanupCtx)
		}()

		// Collect health checks from all modules.
		for _, m := range allModules {
			b.healthChecks = append(b.healthChecks, m.HealthChecks()...)
		}
	}

	// 9. Seed early exit — if WithSeed was configured and --seed was passed,
	// the database module completed seeding during Init. Exit cleanly now.
	if dbMod != nil && dbMod.SeedExit() {
		return nil
	}

	// 10. Router — build HTTP handler with all infrastructure available.
	// lateBgs collects goroutines registered via infra.Background() inside routerFn.
	// A mutex protects concurrent access, and a frozen flag prevents calls after
	// routerFn returns (the goroutines would be lost).
	var lateBgs []bgSpec
	var lateBgsMu sync.Mutex
	var lateBgsFrozen bool
	storageMgr := buildStorageManager(b.storageSpecs)

	infra := Infrastructure{
		Logger:         logger,
		ServerTLS:      serverTLS,
		RateLimiter:    rl,
		KeyedLimiters:  keyedLimiters,
		Storage:        b.storageBackend,
		StorageManager: storageMgr,
		Cron:           cronScheduler,
		AuditLog:       auditLogger,
		EventBus:       eventBus,
		Config:         b.cfg,
		Background: func(name string, fn func(ctx context.Context) error) {
			lateBgsMu.Lock()
			defer lateBgsMu.Unlock()
			if lateBgsFrozen {
				panic("app: Background() must only be called synchronously within RouterFunc")
			}
			lateBgs = append(lateBgs, bgSpec{name: name, fn: fn})
		},
		SetCustomReadiness: func(h http.Handler) {
			lateBgsMu.Lock()
			defer lateBgsMu.Unlock()
			if lateBgsFrozen {
				panic("app: SetCustomReadiness() must only be called synchronously within RouterFunc")
			}
			b.customReadiness = h
		},
		AddHealthCheck: func(check health.DependencyCheck) {
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

	// Register gRPC health service with the same checker used for HTTP readiness.
	// Scan allModules for a *grpcModule — it may come from WithModule(NewGRPCModule(...)).
	for _, m := range allModules {
		if gm, ok := m.(*grpcModule); ok {
			gm.RegisterHealth(healthChecker)
			break
		}
	}

	var readiness http.Handler
	if b.customReadiness != nil {
		readiness = b.customReadiness
	} else {
		readiness = healthhttp.Handler(healthChecker)
	}
	var internalOpts []healthhttp.InternalHandlerOption
	for _, m := range allModules {
		if sm, ok := m.(*sloModule); ok && sm.Checker() != nil {
			internalOpts = append(internalOpts, healthhttp.WithSLOHandler(slohttp.Handler(sm.Checker())))
			break
		}
	}
	internalHandler := healthhttp.NewInternalHandler(b.version, readiness, internalOpts...)
	internalSrv := httpx.NewServer(b.cfg.Internal.Addr(), internalHandler)
	internalErrCh := make(chan error, 1)
	go func() {
		if srvErr := internalSrv.ListenAndServe(); srvErr != nil && !errors.Is(srvErr, http.ErrServerClosed) {
			internalErrCh <- srvErr
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := internalSrv.Shutdown(shutdownCtx); shutdownErr != nil {
			logger.Warn("internal server shutdown error", "error", shutdownErr)
		}
	}()

	// Brief pause to detect early bind failures (port conflict, permission denied).
	select {
	case srvErr := <-internalErrCh:
		return fmt.Errorf("internal server failed to start: %w", srvErr)
	case <-time.After(50 * time.Millisecond):
	}
	logger.Info("internal server started", "addr", b.cfg.Internal.Addr())

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

	// 12. Shutdown hooks — run when the Runner's context is cancelled.
	// Each hook runs with individual panic recovery and a per-hook timeout
	// to prevent a single misbehaving hook from blocking the entire shutdown.
	// WARNING: Infrastructure connections (DB, Redis, Broker) may already be
	// closed when hooks execute — hooks run concurrently with component
	// shutdown. Do not rely on infrastructure being available in hooks.
	if len(b.shutdownHooks) > 0 {
		runner.AddFunc("shutdown-hooks", func(ctx context.Context) error {
			<-ctx.Done()
			for i, fn := range b.shutdownHooks {
				func(idx int, hook func(context.Context)) {
					defer func() {
						if rec := recover(); rec != nil {
							logger.Error("shutdown hook panicked",
								"hook_index", idx,
								"panic", rec,
							)
						}
					}()
					hookCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					done := make(chan struct{})
					go func() {
						defer close(done)
						hook(hookCtx)
					}()
					select {
					case <-done:
					case <-hookCtx.Done():
						logger.Error("shutdown hook timed out", "hook_index", idx)
					}
				}(i, fn)
			}
			return nil
		})
	}

	// 13. gRPC server — added before the public HTTP server so it is
	// stopped after HTTP during graceful shutdown (reverse order).
	for _, m := range allModules {
		if gm, ok := m.(*grpcModule); ok {
			runner.AddFunc("grpc-server", func(ctx context.Context) error {
				return gm.serve(ctx)
			})
			break
		}
	}

	// 14. Public server — added last so it is stopped first (reverse order).
	srvOpts := make([]httpx.ServerOption, 0, len(b.serverOpts)+1)
	if serverTLS != nil {
		srvOpts = append(srvOpts, httpx.WithTLSConfig(serverTLS))
	}
	srvOpts = append(srvOpts, b.serverOpts...)
	srv := httpx.NewServer(b.cfg.Server.Addr(), httpHandler, srvOpts...)
	runner.Add("public-server", lifecycle.HTTPServer(srv))

	// 15. Run — signal handling, component lifecycle, graceful shutdown.
	return runner.Run(context.Background())
}

