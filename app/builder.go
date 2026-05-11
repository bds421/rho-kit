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

	kitauthz "github.com/bds421/rho-kit/authz/v2"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/crypto/v2/paseto"
	"github.com/bds421/rho-kit/data/v2/actionlog"
	"github.com/bds421/rho-kit/data/v2/approval"
	"github.com/bds421/rho-kit/data/v2/budget"
	kitflags "github.com/bds421/rho-kit/flags/v2"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/healthhttp"
	httpxbudget "github.com/bds421/rho-kit/httpx/v2/middleware/budget"
	mwrl "github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit"
	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
	"github.com/bds421/rho-kit/httpx/v2/middleware/stack"
	httpxtenant "github.com/bds421/rho-kit/httpx/v2/middleware/tenant"
	"github.com/bds421/rho-kit/httpx/v2/slohttp"
	"github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
	kitredis "github.com/bds421/rho-kit/infra/redis/v2"
	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/observability/v2/auditlog"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/observability/v2/slo"
	"github.com/bds421/rho-kit/observability/v2/tracing"
	kitcron "github.com/bds421/rho-kit/runtime/v2/cron"
	"github.com/bds421/rho-kit/runtime/v2/eventbus"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// Builder configures and runs a service's infrastructure lifecycle.
//
// Import cost: The app package imports database, redis, messaging, and storage
// modules. Services that only need HTTP+Redis still pull in pgx and AMQP as
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

	// Postgres (pgx-native). v2 dropped GORM and MySQL/MariaDB; pgx is
	// the single supported driver.
	pgxCfg *pgxbackend.Config

	// Redis
	redisOpts     *goredis.Options
	redisConnOpts []kitredis.ConnOption

	// RabbitMQ
	mqURL              string
	criticalBroker     bool
	messageSizeLimiter messaging.MessageSizeLimiter

	// NATS JetStream — independent of RabbitMQ; both may coexist.
	natsCfg *natsbackend.Config

	// JWT
	jwksURL           string
	jwtIssuer         string
	jwtAudience       string
	jwtAllowAnyIssuer bool

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

	// Authorization decider (optional). Exposed via Infrastructure
	// for handler-level RequirePermission wiring.
	authz kitauthz.Decider

	// Feature-flag provider (optional). The Builder constructs a
	// flags.Client around it at Run time and exposes the client on
	// Infrastructure.Flags so handlers can call infra.Flags.Bool /
	// .String / .Int / .Float without per-handler SDK setup.
	flagsProvider kitflags.Provider

	// Production-safety opt-outs. Each one is a deliberate, documented
	// escape hatch from a specific always-on tightening. The validator
	// requires an explicit per-relaxation acknowledgement rather than a
	// blanket "trust me" — there is no global "dev mode" toggle.
	allowInternalNonLoopback bool // C-1: lets Internal.Host == "0.0.0.0" pass.
	allowPlaintext           bool // C-2: lets TLS-disabled deployments pass.
	jwtAllowAnyAudience      bool // H-5: lets WithJWT pass without WithJWTAudience.
	tlsOptionalClientCert    bool // FR-014: lets gateway-fronted services accept clients without certs.

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

	// Migrations applied during pgxModule Init via goose. Optional —
	// services that manage migrations out-of-band leave this nil.
	migrationsDir fs.FS

	// Tracing
	tracingCfg *tracing.Config

	// Server options
	serverOpts []httpx.ServerOption

	// Public gRPC health is off by default. Internal readiness is served on the
	// internal ops listener, including the gRPC Health Checking Protocol.
	publicGRPCHealth bool

	// Public-mux default stack
	disableDefaultStack bool
	stackOpts           []stack.Option

	// Health checks
	healthChecks    []health.DependencyCheck
	customReadiness http.Handler

	// Background goroutines registered before Run
	earlyBgs []bgSpec

	// Shutdown hooks
	shutdownHooks []func(context.Context)

	// startupTimeout caps how long module initialization may take
	// (FR-013). Zero means [defaultStartupTimeout]. Override via
	// [Builder.WithStartupTimeout] for services with genuinely slow
	// boot dependencies (e.g. Postgres warmup, KMS key fetch).
	startupTimeout time.Duration

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

// WithInternalNonLoopback acknowledges that the internal ops port
// (health, ready, metrics) is intentionally bound to a non-loopback
// interface (e.g. 0.0.0.0). Without this opt-in, [Builder.Build]
// rejects any configuration where Internal.Host resolves to "0.0.0.0",
// because /metrics is unauthenticated and exposing it on a routable
// interface leaks Prometheus labels (route patterns, tenant IDs, process
// fingerprinting) to anyone on the network.
//
// Use this only when the operator has confirmed network isolation
// (NetworkPolicy, security group, host-only Docker network) for the
// internal port. The check is unconditional — there is no KIT_ENV
// escape hatch.
func (b *Builder) WithInternalNonLoopback() *Builder {
	b.allowInternalNonLoopback = true
	return b
}

// WithoutTLS acknowledges that the public HTTP server will run without
// TLS. Without this opt-in, [Builder.Build] rejects any configuration
// where [netutil.TLSConfig.Enabled] is false, because partial TLS
// configuration (one missing env var) silently downgrades to plaintext
// HTTP.
//
// Use this only for services explicitly fronted by an external TLS
// terminator (identity proxy, load balancer, ingress controller) that
// re-encrypts to the cluster. The check is unconditional — there is no KIT_ENV
// escape hatch.
func (b *Builder) WithoutTLS() *Builder {
	b.allowPlaintext = true
	return b
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

// WithOptionalClientCertificates opts the public TLS server out of
// the kit's default of requiring a client certificate from every
// caller (mTLS). After this call the listener verifies any presented
// client certificate but does NOT reject anonymous clients
// (tls.VerifyClientCertIfGiven).
//
// Audit FR-014 [HIGH]: pre-fix the Builder constructed every TLS
// listener with VerifyClientCertIfGiven by default, contradicting
// the kit's documented "TLS env enables global mTLS" convention.
// Now mTLS is enforced unless the operator explicitly downgrades —
// a deliberate, documented escape hatch matching the [WithoutTLS]
// shape.
//
// Use this only for services genuinely fronted by an external TLS
// terminator (identity proxy, load balancer, ingress controller) that
// re-encrypts to the cluster *without* presenting a client certificate. Internal
// service-to-service listeners should never use this option — the
// verifier is the kit's only authentication layer for those callers.
func (b *Builder) WithOptionalClientCertificates() *Builder {
	b.tlsOptionalClientCert = true
	return b
}

// WithoutJWTAudience opts out of audience enforcement explicitly.
// Without this opt-in, [Builder.Build] rejects configurations that
// call [Builder.WithJWT] without also calling [Builder.WithJWTAudience],
// because absent audience pinning a token minted for a sibling service
// that trusts the same JWKS is silently valid — the standard JWT
// confused-deputy mitigation (RFC 7519 §4.1.3).
//
// Use this only for genuinely multi-audience deployments. The check
// is unconditional — there is no KIT_ENV escape hatch.
func (b *Builder) WithoutJWTAudience() *Builder {
	b.jwtAllowAnyAudience = true
	return b
}

// WithPostgres configures a pgx-native PostgreSQL pool. v2 dropped
// MySQL/MariaDB and GORM — pgx is the single supported driver, with
// LISTEN/NOTIFY, COPY, and pipelined queries available natively.
//
// Use [WithMigrations] to attach goose-managed migrations.
//
// Panics if cfg.DSN is empty. In non-dev, sslmode must be
// require/verify-ca/verify-full (enforced inside the pgx package's
// Connect).
func (b *Builder) WithPostgres(cfg pgxbackend.Config) *Builder {
	if cfg.DSN == "" {
		panic("app: WithPostgres requires a non-empty DSN")
	}
	b.pgxCfg = &cfg
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
	for _, opt := range connOpts {
		if opt == nil {
			panic("app: WithRedis connection option must not be nil")
		}
	}
	b.redisOpts = cloneRedisOptions(opts)
	b.redisConnOpts = append([]kitredis.ConnOption(nil), connOpts...)
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

// WithMaxMessageBytes sets the default serialized message-size limit for
// Builder-created RabbitMQ and NATS publishers. The default is
// messaging.DefaultMaxMessageBytes.
func (b *Builder) WithMaxMessageBytes(maxBytes int) *Builder {
	b.messageSizeLimiter = b.messageSizeLimiter.WithDefaultMaxBytes(maxBytes)
	return b
}

// WithoutMessageSizeLimit disables the default size limit for Builder-created
// publishers. Route-specific limits configured with WithRouteMaxMessageBytes
// still apply.
func (b *Builder) WithoutMessageSizeLimit() *Builder {
	b.messageSizeLimiter = b.messageSizeLimiter.WithoutDefaultMaxBytes()
	return b
}

// WithRouteMaxMessageBytes overrides the serialized message-size limit for one
// exact exchange+routing-key pair on Builder-created RabbitMQ and NATS
// publishers. routingKey may be empty for fanout-style routes.
func (b *Builder) WithRouteMaxMessageBytes(exchange, routingKey string, maxBytes int) *Builder {
	b.messageSizeLimiter = b.messageSizeLimiter.WithRouteMaxBytes(exchange, routingKey, maxBytes)
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
	if err := natsbackend.ValidateURL(cfg.URL); err != nil {
		panic("app: WithNATS requires a valid URL")
	}
	cfg = mustCloneNATSConfig(cfg)
	b.natsCfg = &cfg
	return b
}

// WithJWT configures a JWKS provider for JWT verification.
// Panics if jwksURL is empty — use environment variables to conditionally skip.
//
// IMPORTANT: pair with [Builder.WithJWTIssuer] and [Builder.WithJWTAudience].
// [Builder.Build] always rejects a configuration where jwksURL is set but
// neither WithJWTIssuer nor [Builder.WithoutJWTIssuer] (and likewise for
// audience) has been called — silently disabling issuer/audience enforcement
// was a known foot-gun in earlier versions and is now an explicit declaration.
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
// Mutually exclusive with [Builder.WithoutJWTIssuer]; the last call wins.
func (b *Builder) WithJWTIssuer(iss string) *Builder {
	if iss == "" {
		panic("app: WithJWTIssuer requires a non-empty issuer (use WithoutJWTIssuer to opt out)")
	}
	b.jwtIssuer = iss
	b.jwtAllowAnyIssuer = false
	return b
}

// WithJWTAudience sets the expected `aud` claim. Tokens whose audience
// does not match are rejected.
//
// Empty input panics — call [Builder.WithoutJWTAudience] explicitly to
// opt out of audience enforcement instead. Earlier versions silently
// accepted "" and degraded to the same "any audience" behavior, which
// hid mis-templated env vars (`WithJWTAudience(os.Getenv("AUD"))` with
// AUD unset) under the same code path as a deliberate opt-out.
func (b *Builder) WithJWTAudience(aud string) *Builder {
	if aud == "" {
		panic("app: WithJWTAudience requires a non-empty audience (use WithoutJWTAudience to opt out)")
	}
	b.jwtAudience = aud
	b.jwtAllowAnyAudience = false
	return b
}

// WithoutJWTIssuer opts out of issuer enforcement explicitly. Use only
// for first-party tokens issued by a trusted internal service where the
// JWKS endpoint is itself authenticated. Required to satisfy
// [Builder.Build]'s always-on guardrail when [Builder.WithJWTIssuer] is
// not used. The check is unconditional — there is no KIT_ENV escape
// hatch.
func (b *Builder) WithoutJWTIssuer() *Builder {
	b.jwtAllowAnyIssuer = true
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
	for _, opt := range opts {
		if opt == nil {
			panic("app: WithSignedRequests option must not be nil")
		}
	}
	b.signedSpec = &signedRequestSpec{
		resolver: resolver,
		store:    store,
		opts:     append([]signedrequest.Option(nil), opts...),
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
// `required` controls whether requests without a tenant are
// rejected with 400 — applied to every method by default
// (including GET/HEAD/OPTIONS). Health/readiness probes belong on
// the kit's internal ops port, which is a separate listener and
// never sees the tenant middleware. Use
// [Builder.WithAllowMissingTenantOnSafeMethods] only when the
// public mux must serve pre-auth GETs alongside tenant-scoped
// routes.
//
// Cache and idempotency wrappers ([data/cache/tenant.Wrap] /
// [data/idempotency/tenant.Wrap]) are caller-applied — the
// Builder doesn't own those instances and shouldn't silently
// rewrite them.
func (b *Builder) WithMultiTenant(extractor httpxtenant.Extractor, required bool) *Builder {
	b.tenantSpec = &tenantSpec{extractor: extractor, required: required}
	return b
}

// WithAllowMissingTenantOnSafeMethods opts out of the default
// require-tenant-on-every-method rule for GET/HEAD/OPTIONS. Forwards
// to [httpxtenant.WithAllowMissingTenantOnSafeMethods].
//
// Mutually exclusive with [Builder.WithTenantBudget]: budget
// enforcement keys on the tenant ID, so every charged route needs a
// required tenant context before the budget middleware runs.
// [Builder.Validate] rejects the combination at startup.
func (b *Builder) WithAllowMissingTenantOnSafeMethods() *Builder {
	if b.tenantSpec == nil {
		panic("app: WithAllowMissingTenantOnSafeMethods must be called after WithMultiTenant")
	}
	b.tenantSpec.allowMissingTenantOnSafeMethods = true
	return b
}

// WithTenantBudget enforces a per-tenant cost budget on every
// inbound request via [httpx/middleware/budget]. The default key
// function pulls the tenant ID from ctx (assumes
// [WithMultiTenant] is also configured with required=true); supply a
// custom one via [httpxbudget.WithKeyFunc] if your scope is different.
//
// Panics if `b2` is nil — silent no-budget would defeat the
// kit's "refuse to misconfigure" stance.
func (b *Builder) WithTenantBudget(b2 budget.Budget, opts ...httpxbudget.Option) *Builder {
	if b2 == nil {
		panic("app: WithTenantBudget requires a non-nil Budget store")
	}
	for _, opt := range opts {
		if opt == nil {
			panic("app: WithTenantBudget option must not be nil")
		}
	}
	b.budgetSpec = &budgetSpec{store: b2, opts: append([]httpxbudget.Option(nil), opts...)}
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

// WithAuthz registers an [authz.Decider] (the kit's vendor-neutral
// authorization seam — OpenFGA, Cedar, Casbin, or the in-memory
// adapter for tests). The decider is exposed on
// [Infrastructure.Authz] so handlers can build per-route policies via
// [httpx/authz.FromDecider] and [httpx/authz.RequirePermission].
//
// The Builder does NOT auto-apply authz to the public mux because
// authorization needs per-route subject + resource extractors that
// depend on the route's parameter shape. The middleware lives at the
// route level, not the mux level.
//
// Panics if `d` is nil.
func (b *Builder) WithAuthz(d kitauthz.Decider) *Builder {
	if d == nil {
		panic("app: WithAuthz requires a non-nil Decider")
	}
	b.authz = d
	return b
}

// WithFeatureFlags registers an OpenFeature-compatible provider
// (LaunchDarkly, flagd, GrowthBook, or the kit's in-memory adapter
// for tests). The Builder wraps it in a [flags.Client] at Run time
// and exposes the client on [Infrastructure.Flags] so handlers can
// gate on feature flags without per-handler SDK setup.
//
// The client auto-populates evaluation context from
// [tenant.FromContext] and [contextutil.CorrelationID], so per-tenant
// flag rollouts work without extra boilerplate at every flag check.
//
// Panics if `p` is nil.
func (b *Builder) WithFeatureFlags(p kitflags.Provider) *Builder {
	if p == nil {
		panic("app: WithFeatureFlags requires a non-nil Provider")
	}
	b.flagsProvider = p
	return b
}

// WithIPRateLimit configures a per-IP rate limiter and auto-applies
// its middleware to the public mux. The limiter sits between
// stack.Default and signedrequest in the inbound chain — cheap-reject
// hostile clients before the signed-request crypto verification runs.
//
// Lifecycle note: the limiter's background sweeper runs via
// runner.AddFunc. A panic inside that goroutine kills the entire service
// via the lifecycle Runner — there is no per-component supervision.
// Monitor the "goroutine_panicked" log event if you need an early signal.
//
// The limiter is also exposed on Infrastructure.RateLimiter for
// per-route overrides — e.g. tightening the limit on /admin while the
// auto-applied limiter handles the public mux baseline.
func (b *Builder) WithIPRateLimit(requests int, window time.Duration) *Builder {
	if requests <= 0 {
		panic("app: WithIPRateLimit requires a positive request limit")
	}
	if window <= 0 {
		panic("app: WithIPRateLimit requires a positive window")
	}
	b.ipRateRequests = requests
	b.ipRateWindow = window
	return b
}

// WithKeyedRateLimit adds a named keyed rate limiter. The limiter is accessible
// via Infrastructure.KeyedLimiters[name].
func (b *Builder) WithKeyedRateLimit(name string, requests int, window time.Duration) *Builder {
	if name == "" {
		panic("app: WithKeyedRateLimit requires a non-empty name")
	}
	if requests <= 0 {
		panic("app: WithKeyedRateLimit requires a positive request limit")
	}
	if window <= 0 {
		panic("app: WithKeyedRateLimit requires a positive window")
	}
	for _, existing := range b.keyedLimiters {
		if existing.name == name {
			panic("app: duplicate keyed rate limiter name")
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
	validateDependencyChecks(checks, "WithStorage")
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
	validateDependencyChecks(checks, "WithNamedStorage")
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
	for _, opt := range opts {
		if opt == nil {
			panic("app: WithAuditLog option must not be nil")
		}
	}
	b.auditStore = store
	b.auditOpts = append([]auditlog.Option(nil), opts...)
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
	for _, opt := range opts {
		if opt == nil {
			panic("app: WithCron option must not be nil")
		}
	}
	b.cronEnabled = true
	b.cronOpts = append([]kitcron.Option(nil), opts...)
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

// WithMigrations configures goose SQL migrations. Requires WithPostgres
// — the migrations run via the pgx pool inside the pgx module's Init.
//
// Migrations always run regardless of environment. This ensures dev,
// staging, and production use the same schema migration path.
func (b *Builder) WithMigrations(dir fs.FS) *Builder {
	if dir == nil {
		panic("app: WithMigrations requires a non-nil fs.FS")
	}
	b.migrationsDir = dir
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
	if opt == nil {
		panic("app: WithServerOption requires a non-nil option")
	}
	b.serverOpts = append(b.serverOpts, opt)
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

// WithCustomReadiness overrides the auto-accumulated health checks with a
// custom readiness handler (e.g. for custom state introspection).
func (b *Builder) WithCustomReadiness(h http.Handler) *Builder {
	if h == nil {
		panic("app: WithCustomReadiness requires a non-nil handler")
	}
	b.customReadiness = h
	return b
}

// WithStackOptions appends options forwarded to [stack.Default] when the
// Builder wraps the public mux. Examples: [stack.WithQuietPaths],
// [stack.WithoutTimeout], [stack.WithRecoverMetrics]. The Builder always
// supplies a logger derived from the slog default; pass [stack.WithLogger]
// here to override it.
func (b *Builder) WithStackOptions(opts ...stack.Option) *Builder {
	for _, opt := range opts {
		if opt == nil {
			panic("app: WithStackOptions option must not be nil")
		}
	}
	b.stackOpts = append(b.stackOpts, opts...)
	return b
}

// WithoutDefaultStack disables the auto-applied public-mux middleware
// stack (recover, security headers, metrics, request ID, correlation ID,
// tracing, logging, timeout, request logger). Use only when the service
// supplies its own equivalent chain — services that omit this without a
// replacement run without panic recovery, structured logs, or per-request
// timeouts. Reserved for tests and bespoke transports.
func (b *Builder) WithoutDefaultStack() *Builder {
	b.disableDefaultStack = true
	return b
}

// Background registers a managed goroutine that starts before the router.
// If fn returns a non-nil error, the entire service shuts down.
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
// generous default — tighten via [Builder.WithStartupTimeout] for
// services with strict cold-start budgets.
const defaultStartupTimeout = 60 * time.Second

// WithStartupTimeout caps module initialization time. Omit this option to use
// [defaultStartupTimeout].
//
// FR-013 [MED]: pre-fix module Init ran with context.Background, so
// a module that hung during initialization (KMS DNS, broker connect)
// blocked startup forever. The deadline now triggers a typed error
// from the affected module's Init.
func (b *Builder) WithStartupTimeout(d time.Duration) *Builder {
	if d <= 0 {
		panic("app: WithStartupTimeout requires a positive duration")
	}
	b.startupTimeout = d
	return b
}

// WithPublicGRPCHealth also registers the gRPC Health Checking Protocol on
// the public gRPC listener.
//
// By default, Builder exposes health on the internal ops listener only:
// HTTP /ready plus gRPC health over h2c on the same internal address. Use this
// opt-in only when the public gRPC listener is protected by network policy or
// the health service is intentionally part of the public contract.
func (b *Builder) WithPublicGRPCHealth() *Builder {
	b.publicGRPCHealth = true
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
			panic("app: duplicate module name")
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
// (DB, MQ, tracing) uses defers. The internal health server is started outside
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
	builtinModules := b.buildIntegrationModules()
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
	serverTLS, err := b.cfg.TLS.ServerTLS(b.serverTLSOptions()...)
	if err != nil {
		return fmt.Errorf("build server TLS config: %w", err)
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

	// 2.5. EventBus lifecycle -- register even when the Builder uses
	// the eventbus package's default auto-started worker pool. Start
	// will not create duplicate workers for that pool; it simply binds
	// shutdown to the Runner so embedded RunContext calls do not leak
	// eventbus workers after returning.
	runner.Add("eventbus", eventBus)

	// 3. Rate limiters
	var rl *mwrl.RateLimiter
	if b.ipRateRequests > 0 {
		rl = mwrl.NewRateLimiter(b.ipRateRequests, b.ipRateWindow)
		runner.AddFunc("rate-limiter-cleanup", func(ctx context.Context) error {
			return rl.Run(ctx)
		})
	}

	keyedLimiters := make(map[string]*mwrl.KeyedRateLimiter, len(b.keyedLimiters))
	for _, spec := range b.keyedLimiters {
		kl := mwrl.NewKeyedRateLimiter(spec.requests, spec.window)
		keyedLimiters[spec.name] = kl
		name := "keyed-limiter-" + spec.name
		runner.AddFunc(name, func(ctx context.Context) error {
			return kl.Run(ctx)
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
	//
	// Pre-Init pass: inject the kit-level serverTLS into the gRPC
	// module so its grpc.Server is constructed with the same TLS
	// surface as the HTTP server. Without this, services that set
	// TLS_CERT/TLS_KEY would silently run plaintext gRPC alongside
	// TLS HTTP — an authentication bypass for any service relying on
	// "if I'm in mTLS mode, peers are authenticated".
	if serverTLS != nil {
		for _, m := range allModules {
			if gm, ok := m.(*grpcModule); ok {
				gm.setTLSConfig(serverTLS)
				break
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
			logger,
			runner,
			b.cfg,
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
		Authz:          b.authz,
		Flags:          b.flagsClient(),
		Config:         b.cfg,
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
	// Compose the inbound middleware chain from the inside out:
	//
	//   1. budget — charges per-tenant; furthest from the network
	//      so rejections still see tenant ctx populated.
	//   2. tenant — extracts tenant ID into ctx for budget + handler.
	//   3. signedrequest — applied next; unsigned requests get rejected
	//      before tenant or budget work runs.
	//   4. ratelimit (per-IP) — cheap reject hostile clients before
	//      the signedrequest crypto verification runs. Auto-applied
	//      iff WithIPRateLimit was configured.
	//   5. stack.Default — outermost; recover, security headers, metrics,
	//      request ID, correlation ID, tracing, logging, timeout, request
	//      logger. This means panics anywhere downstream still convert to
	//      500 + structured log, and every request lands with a bounded
	//      ctx, observability headers, and a scoped logger.
	if mw := b.budgetMiddleware(); mw != nil {
		httpHandler = mw(httpHandler)
	}
	if mw := b.tenantMiddleware(); mw != nil {
		httpHandler = mw(httpHandler)
	}
	if mw := b.signedRequestMiddleware(); mw != nil {
		httpHandler = mw(httpHandler)
	}
	if rl != nil {
		httpHandler = rl.Middleware(httpHandler)
	}
	if !b.disableDefaultStack {
		// FR-009 [MED]: pass the resolved logger so the request stack uses
		// the same logger as infrastructure setup. Pre-fix this hardcoded
		// slog.Default(), so [Builder.WithLogger] silently failed to take
		// effect on the public middleware chain.
		httpHandler = stack.Default(httpHandler, logger, b.stackOpts...)
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

	// gRPC health is internal by default. Public registration is an
	// explicit opt-in because the public gRPC listener may be reachable by
	// untrusted clients even when the internal ops listener is not.
	if b.publicGRPCHealth {
		for _, m := range allModules {
			if gm, ok := m.(*grpcModule); ok {
				gm.RegisterHealth(healthChecker)
				break
			}
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
	serverErrorLogOpt := serverErrorLogOption(logger)
	internalHandler := healthhttp.NewInternalHandler(b.version, readiness, internalOpts...)
	internalHandler = withInternalGRPCHealth(internalHandler, healthChecker)
	internalSrv := httpx.NewServer(b.cfg.Internal.Addr(), internalHandler, serverErrorLogOpt)
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
	logger.Info("internal server started", redact.String("addr", b.cfg.Internal.Addr()))

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

	// 12. Shutdown hooks fire from the Runner's BeforeStop callback (wired in
	// step 2) — synchronously after ctx cancels but BEFORE any component
	// Stop runs. Hooks see live DB / broker / cache connections; the
	// previous design ran them concurrently with stopAll which silently
	// observed closed connections.

	// 13. gRPC server — added before the public HTTP server so it is
	// stopped after HTTP during graceful shutdown (reverse order).
	for _, m := range allModules {
		if gm, ok := m.(*grpcModule); ok {
			runner.Add("grpc-server", gm)
			break
		}
	}

	// 14. Public server — added last so it is stopped first (reverse order).
	srvOpts := make([]httpx.ServerOption, 0, len(b.serverOpts)+2)
	srvOpts = append(srvOpts, serverErrorLogOpt)
	if serverTLS != nil {
		srvOpts = append(srvOpts, httpx.WithTLSConfig(serverTLS))
	}
	srvOpts = append(srvOpts, b.serverOpts...)
	srv := httpx.NewServer(b.cfg.Server.Addr(), httpHandler, srvOpts...)
	runner.Add("public-server", lifecycle.HTTPServer(srv))

	// 15. Run — signal handling, component lifecycle, graceful shutdown.
	return runner.Run(ctx)
}
