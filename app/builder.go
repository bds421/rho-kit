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

	"github.com/bds421/rho-kit/crypto/paseto"
	"github.com/bds421/rho-kit/data/actionlog"
	"github.com/bds421/rho-kit/data/approval"
	"github.com/bds421/rho-kit/data/budget"
	"github.com/bds421/rho-kit/httpx"
	"github.com/bds421/rho-kit/httpx/healthhttp"
	httpxbudget "github.com/bds421/rho-kit/httpx/middleware/budget"
	mwrl "github.com/bds421/rho-kit/httpx/middleware/ratelimit"
	"github.com/bds421/rho-kit/httpx/middleware/signedrequest"
	httpxtenant "github.com/bds421/rho-kit/httpx/middleware/tenant"
	"github.com/bds421/rho-kit/httpx/slohttp"
	"github.com/bds421/rho-kit/infra/leaderelection"
	"github.com/bds421/rho-kit/infra/messaging/natsbackend"
	kitredis "github.com/bds421/rho-kit/infra/redis"
	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormmysql"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx"
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
//
// Failure semantics: [Builder.Build] (and the panicking With*() helpers)
// must be treated as fatal. Builder does not roll back partially-acquired
// resources on failure — if WithPostgres opens a pool and WithRedis then
// panics, the pool is leaked. Callers must let the process exit; do NOT
// catch the panic and retry. The Builder is a composition root, not a
// reusable factory.
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

	// pgx-native Postgres (alternative to WithPostgres). Validate
	// rejects the combination — the two represent different drivers
	// against the same DB and would race on connection state.
	pgxCfg *pgxbackend.Config

	// Redis
	redisOpts     *goredis.Options
	redisConnOpts []kitredis.ConnOption

	// RabbitMQ
	mqURL          string
	criticalBroker bool

	// NATS JetStream — independent of RabbitMQ; both may coexist.
	natsCfg *natsbackend.Config

	// JWT
	jwksURL          string
	jwtIssuer        string
	jwtAudience      string
	jwtAllowAnyIssue bool

	// PASETO (alternative to JWT). Caller-constructed Provider, so the
	// kit does not impose a particular key source.
	pasetoProvider *paseto.Provider

	// Leader election (optional). When set, cron jobs gate on
	// elector.IsLeader() so only the elected replica runs scheduled
	// work. Other infrastructure (HTTP, consumers) keeps running on
	// every replica.
	leaderElector leaderelection.Elector

	// Signed requests (optional). The public mux wraps every route in
	// the signedrequest middleware when this is set.
	signedSpec *signedRequestSpec

	// Multi-tenant request handling (optional). Activates the tenant
	// middleware so handlers can read tenant.FromContext.
	tenantSpec *tenantSpec

	// Per-tenant cost budgets (optional). The public mux charges every
	// request against the tenant's bucket when set.
	budgetSpec *budgetSpec

	// Append-only action log (optional). Exposed via Infrastructure.
	alog actionlog.Logger

	// Approval store (optional). Exposed via Infrastructure.
	astore approval.Store

	// Production-defaults switch — see Builder.WithProductionDefaults.
	productionDefaults bool

	// Production-defaults opt-outs. Each one is a deliberate, documented
	// escape hatch from a specific tightening. Keep them isolated from
	// productionDefaults itself so the validator can require an explicit
	// acknowledgement per relaxation rather than a blanket "trust me".
	allowProdInternalExposed bool // C-1: lets Internal.Host == "0.0.0.0" pass.
	allowProdPlaintext       bool // C-2: lets TLS-disabled deployments pass.
	jwtAllowAnyAudience      bool // H-5: lets WithJWT pass without WithJWTAudience.

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

// WithProductionDefaults flips the Builder into "production-shape"
// validation mode. The toggle does not by itself reconfigure existing
// options; instead it adds startup checks that fail loudly when a
// production-required configuration knob is missing.
//
// Tightenings (validated at [Builder.Build] time):
//
//   - JWT: WithJWT must be paired with WithJWTIssuer or
//     WithJWTAllowAnyIssuer. The legacy "https://oathkeeper" default
//     is not allowed in production.
//   - Postgres: sslmode must be one of {require, verify-ca, verify-full}.
//   - Tracing: SampleRate ≤ 0.1 unless explicitly overridden — full
//     sampling is a collector-cost foot-gun.
//
// Calling WithProductionDefaults outside KIT_ENV=production logs a
// warning but is otherwise allowed — useful for staging environments
// that must mirror prod, or for local-dev integration tests that
// want to flush out config gaps before they hit a real deployment.
//
// Each individual tightening is also enforceable standalone (the
// jwtModule's KIT_ENV=production panic is independent of this
// switch). The aggregate option is the right choice for any service
// that aspires to a boring production stance.
func (b *Builder) WithProductionDefaults() *Builder {
	b.productionDefaults = true
	return b
}

// WithProductionInternalExposed acknowledges that the internal ops port
// (health, ready, metrics) is intentionally bound to a non-loopback
// interface (e.g. 0.0.0.0). Without this opt-in, [Builder.WithProductionDefaults]
// rejects any configuration where Internal.Host resolves to "0.0.0.0",
// because /metrics is unauthenticated and exposing it on a routable
// interface leaks Prometheus labels (route patterns, tenant IDs, process
// fingerprinting) to anyone on the network.
//
// Use this only when the operator has confirmed network isolation
// (NetworkPolicy, security group, host-only Docker network) for the
// internal port.
func (b *Builder) WithProductionInternalExposed() *Builder {
	b.allowProdInternalExposed = true
	return b
}

// WithProductionAllowPlaintext acknowledges that the public HTTP server
// will run without TLS in production. Without this opt-in,
// [Builder.WithProductionDefaults] rejects any configuration where
// [netutil.TLSConfig.Enabled] is false, because partial TLS configuration
// (one missing env var) silently downgrades to plaintext HTTP.
//
// Use this only for services explicitly fronted by an external TLS
// terminator (Oathkeeper, ALB, ingress controller) that re-encrypts
// to the cluster.
func (b *Builder) WithProductionAllowPlaintext() *Builder {
	b.allowProdPlaintext = true
	return b
}

// WithJWTAllowAnyAudience opts out of audience enforcement explicitly.
// Without this opt-in, [Builder.WithProductionDefaults] rejects
// configurations that call [Builder.WithJWT] without also calling
// [Builder.WithJWTAudience], because absent audience pinning a token
// minted for a sibling service that trusts the same JWKS is silently
// valid — the standard JWT confused-deputy mitigation (RFC 7519 §4.1.3).
//
// Use this only for genuinely multi-audience deployments.
func (b *Builder) WithJWTAllowAnyAudience() *Builder {
	b.jwtAllowAnyAudience = true
	return b
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

// WithPgx configures a pgx-native Postgres pool. Use this when the
// service needs LISTEN/NOTIFY, COPY, or pipelined queries that
// `database/sql` (the WithPostgres path) cannot expose.
//
// WithPgx and [WithPostgres] are mutually exclusive — both target
// Postgres but with different drivers, and configuring both
// simultaneously would create two pools competing for the same
// database with different lifecycle semantics. Validate rejects the
// combination at startup.
//
// Panics if cfg.DSN is empty. TLS rules mirror WithPostgres: in
// non-dev, sslmode must be require/verify-ca/verify-full (enforced
// inside the pgx package's Connect).
func (b *Builder) WithPgx(cfg pgxbackend.Config) *Builder {
	if cfg.DSN == "" {
		panic("app: WithPgx requires a non-empty DSN")
	}
	b.pgxCfg = &cfg
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

// WithNATS registers a NATS JetStream broker. The kit exposes the
// connection plus a default Publisher via Infrastructure.NATS and
// Infrastructure.NATSPublisher; stream/consumer declarations are
// caller-driven so the Builder doesn't impose a specific topology.
//
// WithNATS is independent of [Builder.WithRabbitMQ] — both can be
// configured simultaneously when a service publishes to one broker
// and consumes from another. Each is exposed via dedicated
// Infrastructure fields.
//
// Panics if cfg.URL is empty.
func (b *Builder) WithNATS(cfg natsbackend.Config) *Builder {
	if cfg.URL == "" {
		panic("app: WithNATS requires a non-empty URL")
	}
	b.natsCfg = &cfg
	return b
}

// WithJWT configures a JWKS provider for JWT verification.
// Panics if jwksURL is empty — use environment variables to conditionally skip.
//
// IMPORTANT: pair with [Builder.WithJWTIssuer] and (when applicable)
// [Builder.WithJWTAudience]. In KIT_ENV=production, [Builder.Build] panics
// if jwksURL is set but neither WithJWTIssuer nor [Builder.WithJWTAllowAnyIssuer]
// has been called — silently disabling issuer enforcement was a known
// foot-gun in earlier versions.
func (b *Builder) WithJWT(jwksURL string) *Builder {
	if jwksURL == "" {
		panic("app: WithJWT requires a non-empty JWKS URL")
	}
	b.jwksURL = jwksURL
	return b
}

// WithJWTIssuer sets the expected `iss` claim. Tokens with a different
// issuer (or no issuer) are rejected at verification time.
//
// Mutually exclusive with [Builder.WithJWTAllowAnyIssuer]; the last call wins.
func (b *Builder) WithJWTIssuer(iss string) *Builder {
	if iss == "" {
		panic("app: WithJWTIssuer requires a non-empty issuer (use WithJWTAllowAnyIssuer to opt out)")
	}
	b.jwtIssuer = iss
	b.jwtAllowAnyIssue = false
	return b
}

// WithJWTAudience sets the expected `aud` claim. Tokens whose audience
// does not match are rejected. Empty audience accepts any.
func (b *Builder) WithJWTAudience(aud string) *Builder {
	b.jwtAudience = aud
	return b
}

// WithJWTAllowAnyIssuer opts out of issuer enforcement explicitly. Use
// only for first-party tokens issued by a trusted internal service where
// the JWKS endpoint is itself authenticated. In KIT_ENV=production this
// is required to satisfy [Builder.Build]'s guardrail when WithJWTIssuer is
// not used.
func (b *Builder) WithJWTAllowAnyIssuer() *Builder {
	b.jwtAllowAnyIssue = true
	b.jwtIssuer = ""
	return b
}

// WithPASETO registers a PASETO Provider as the service's token
// verifier. PASETO is the recommended alternative to JWT for new
// internal services — its v4 spec eliminates the algorithm-confusion
// and `alg=none` attack classes by baking exactly one algorithm into
// each (version, purpose) tuple.
//
// The caller constructs the Provider (via [paseto.NewProvider]) so
// the kit is unopinionated about key sourcing — services pull from
// KMS, a JWKS-equivalent endpoint, or a static config file using
// whatever shape fits their deployment.
//
// `WithPASETO` and `WithJWT` are NOT mutually exclusive — a service
// can verify both simultaneously during a migration. New endpoints
// pick one explicitly via the auth middleware they install.
//
// Panics if `p` is nil — pass an explicitly-constructed Provider or
// don't call this method.
func (b *Builder) WithPASETO(p *paseto.Provider) *Builder {
	if p == nil {
		panic("app: WithPASETO requires a non-nil Provider")
	}
	b.pasetoProvider = p
	return b
}

// WithSignedRequests installs the [signedrequest] middleware on the
// public mux so every inbound request must carry a valid HMAC
// signature header (`X-Signature`, plus timestamp and nonce). Use
// this for service-to-service traffic where mTLS isn't available
// but message integrity is required.
//
// `resolver` looks up the HMAC secret for a given key ID; `store`
// caches recently-seen nonces to defeat replay. The middleware is
// strict about defaults — a nil store panics at construction since
// no-store means trivially-replayable signatures.
//
// `opts` flow through to [signedrequest.Middleware] (clock skew,
// required headers, body cap).
func (b *Builder) WithSignedRequests(
	resolver signedrequest.KeyResolver,
	store signedrequest.NonceStore,
	opts ...signedrequest.Option,
) *Builder {
	if resolver == nil {
		panic("app: WithSignedRequests requires a non-nil KeyResolver")
	}
	if store == nil {
		panic("app: WithSignedRequests requires a non-nil NonceStore (no-store means trivially-replayable signatures)")
	}
	b.signedSpec = &signedRequestSpec{
		resolver: resolver,
		store:    store,
		opts:     opts,
	}
	return b
}

// WithMultiTenant activates tenant-aware request handling. The
// public mux gets the tenant middleware (extracts the tenant ID
// from the request and stores it on ctx); handlers downstream
// can call [tenant.FromContext] / [tenant.Required] without
// each route reinventing the extractor.
//
// `extractor` defaults to [httpxtenant.HeaderExtractor("X-Tenant-Id")]
// when nil. Pass a custom one to read from a JWT claim, mTLS
// certificate, or whatever your auth boundary surfaces.
//
// `required` controls whether state-changing requests without a
// tenant are rejected with 400 (the recommended default — see
// the tenant middleware's package doc).
//
// Cache and idempotency wrappers ([data/cache/tenant.Wrap] /
// [data/idempotency/tenant.Wrap]) are caller-applied — the
// Builder doesn't own those instances and shouldn't silently
// rewrite them.
func (b *Builder) WithMultiTenant(extractor httpxtenant.Extractor, required bool) *Builder {
	b.tenantSpec = &tenantSpec{extractor: extractor, required: required}
	return b
}

// WithTenantBudget enforces a per-tenant cost budget on every
// inbound request via [httpx/middleware/budget]. The default key
// function pulls the tenant ID from ctx (assumes
// [WithMultiTenant] is also configured); supply a custom one via
// [httpxbudget.WithKeyFunc] if your scope is different.
//
// Panics if `b2` is nil — silent no-budget would defeat the
// kit's "refuse to misconfigure" stance.
func (b *Builder) WithTenantBudget(b2 budget.Budget, opts ...httpxbudget.Option) *Builder {
	if b2 == nil {
		panic("app: WithTenantBudget requires a non-nil Budget store")
	}
	b.budgetSpec = &budgetSpec{store: b2, opts: opts}
	return b
}

// WithActionLogger registers an [actionlog.Logger] so handlers can
// attribute writes to the originating actor + tenant. Exposed via
// [Infrastructure.ActionLog] — handlers append entries; the kit
// doesn't auto-instrument routes (verb/resource attribution is
// app-specific).
//
// Panics if `l` is nil.
func (b *Builder) WithActionLogger(l actionlog.Logger) *Builder {
	if l == nil {
		panic("app: WithActionLogger requires a non-nil Logger")
	}
	b.alog = l
	return b
}

// WithApprovalStore registers an [approval.Store] so handlers can
// gate destructive operations behind a pending → approved →
// executed lifecycle. Exposed via [Infrastructure.ApprovalStore].
//
// Panics if `s` is nil.
func (b *Builder) WithApprovalStore(s approval.Store) *Builder {
	if s == nil {
		panic("app: WithApprovalStore requires a non-nil Store")
	}
	b.astore = s
	return b
}

// WithIPRateLimit configures a per-IP rate limiter.
//
// Lifecycle note: the limiter's background sweeper runs via
// runner.AddFunc. A panic inside that goroutine kills the entire service
// via the lifecycle Runner — there is no per-component supervision.
// Monitor the "goroutine_panicked" log event if you need an early signal.
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
//
// When [WithLeaderElection] is also configured, every cron job gates on
// `elector.IsLeader()` automatically — only the elected replica runs
// scheduled work. Other infrastructure (HTTP, consumers) keeps running on
// every replica.
func (b *Builder) WithCron(opts ...kitcron.Option) *Builder {
	b.cronEnabled = true
	b.cronOpts = opts
	return b
}

// WithLeaderElection registers an [leaderelection.Elector] that runs
// continuously under the lifecycle runner. The elector's IsLeader()
// is consulted automatically by the cron scheduler: jobs skip when
// the replica is not the leader. Other replicas keep their HTTP
// servers and consumers running normally.
//
// The elector is reachable via Infrastructure.Leader for advanced
// callers (custom worker loops, leader-only routes).
//
// Panics if elector is nil — pass an explicit elector or don't call
// this method.
func (b *Builder) WithLeaderElection(e leaderelection.Elector) *Builder {
	if e == nil {
		panic("app: WithLeaderElection requires a non-nil Elector")
	}
	b.leaderElector = e
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
		opts := append([]kitcron.Option(nil), b.cronOpts...)
		if b.leaderElector != nil {
			opts = append(opts, kitcron.WithLeaderGate(b.leaderElector.IsLeader))
		}
		cronScheduler = kitcron.New(logger, opts...)
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
		TenantBudget:   b.budgetSpecStore(),
		ActionLog:      b.actionLogger(),
		ApprovalStore:  b.approvalStore(),
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
	// Compose the inbound middleware chain from the inside out:
	//
	//   1. budget — charges per-tenant; furthest from the network
	//      so rejections still see tenant ctx populated.
	//   2. tenant — extracts tenant ID into ctx for budget + handler.
	//   3. signedrequest — outermost; unsigned requests get rejected
	//      before any tenant or budget work runs.
	if mw := b.budgetMiddleware(); mw != nil {
		httpHandler = mw(httpHandler)
	}
	if mw := b.tenantMiddleware(); mw != nil {
		httpHandler = mw(httpHandler)
	}
	if mw := b.signedRequestMiddleware(); mw != nil {
		httpHandler = mw(httpHandler)
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
