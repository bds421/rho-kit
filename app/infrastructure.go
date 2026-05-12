package app

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc"

	kitauthz "github.com/bds421/rho-kit/authz/v2"
	"github.com/bds421/rho-kit/crypto/v2/paseto"
	"github.com/bds421/rho-kit/data/v2/actionlog"
	"github.com/bds421/rho-kit/data/v2/approval"
	"github.com/bds421/rho-kit/data/v2/budget"
	kitflags "github.com/bds421/rho-kit/flags/v2"
	"github.com/bds421/rho-kit/httpx/v2"
	mwrl "github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit"
	"github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
	kitredis "github.com/bds421/rho-kit/infra/redis/v2"
	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/observability/v2/auditlog"
	"github.com/bds421/rho-kit/observability/v2/health"
	kitcron "github.com/bds421/rho-kit/runtime/v2/cron"
	"github.com/bds421/rho-kit/runtime/v2/eventbus"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// RouterFunc builds the service's HTTP handler from the initialized
// infrastructure. It is called after all With*() infrastructure is set
// up but before the server starts.
type RouterFunc func(infra Infrastructure) http.Handler

// Infrastructure is the collection of initialized infrastructure components
// passed to the RouterFunc. Nil fields indicate the corresponding With*()
// method was not called.
//
// The callback fields (Background, SetCustomReadiness, AddHealthCheck) are
// only valid during the synchronous execution of RouterFunc. Calling them
// after RouterFunc returns will panic (lateBgsFrozen guard). This is by
// design: goroutines registered after the Builder has started the lifecycle
// would be silently lost. If you need late-bound goroutines, start them
// inside the function passed to Background — that function runs under the
// lifecycle Runner's supervision.
type Infrastructure struct {
	Logger    *slog.Logger
	ClientTLS *tls.Config
	ServerTLS *tls.Config

	// DB is the canonical Postgres pool. v2 dropped MySQL/MariaDB and
	// GORM; pgx is the only supported driver. Configured via WithPostgres.
	DB *pgxbackend.Pool

	Broker    messaging.Connector // nil if no WithRabbitMQ
	Publisher messaging.Publisher // nil if no WithRabbitMQ
	Consumer  messaging.Consumer  // nil if no WithRabbitMQ

	NATS          *natsbackend.Connection // nil if no WithNATS
	NATSPublisher *natsbackend.Publisher  // nil if no WithNATS

	JWT    *jwtutil.Provider // nil if no WithJWT
	PASETO *paseto.Provider  // nil if no WithPASETO

	Leader leaderelection.Elector // nil if no WithLeaderElection

	TenantBudget  budget.Budget    // nil if no WithTenantBudget
	ActionLog     actionlog.Logger // nil if no WithActionLogger
	ApprovalStore approval.Store   // nil if no WithApprovalStore
	Authz         kitauthz.Decider // nil if no WithAuthz
	Flags         *kitflags.Client // nil if no WithFeatureFlags

	RateLimiter   *mwrl.RateLimiter                 // nil if no WithIPRateLimit
	KeyedLimiters map[string]*mwrl.KeyedRateLimiter // populated by WithKeyedRateLimit

	Redis *kitredis.Connection // nil if no WithRedis

	Storage        storage.Storage    // nil if no WithStorage
	StorageManager *storage.Manager   // nil if no WithNamedStorage
	Cron           *kitcron.Scheduler // nil if no WithCron
	AuditLog       *auditlog.Logger   // nil if no WithAuditLog
	EventBus       *eventbus.Bus      // always non-nil; in-process domain event dispatch
	GRPCServer     *grpc.Server       // nil if no NewGRPCModule

	HTTPClient *http.Client
	Config     BaseConfig

	// Background registers a managed goroutine that runs until the worker
	// context is cancelled. If the function returns a non-nil error, the
	// entire service shuts down. Use this inside RouterFunc for late-bound
	// goroutines that need infrastructure references (hub, consumers, etc.).
	Background func(name string, fn func(ctx context.Context) error)

	// SetCustomReadiness overrides the auto-accumulated health checks with a
	// custom readiness handler. Call this inside RouterFunc when the service
	// needs per-component health introspection (e.g., per-observer scan state).
	SetCustomReadiness func(h http.Handler)

	// AddHealthCheck appends a DependencyCheck to the readiness probe.
	// Call this inside RouterFunc when health checks depend on infrastructure
	// created within the router (e.g., transport-specific checks).
	AddHealthCheck func(check health.DependencyCheck)
}

// TestInfrastructure returns an Infrastructure with safe no-op defaults for
// all function fields. Use this in tests to avoid nil-pointer panics when
// testing RouterFunc implementations.
func TestInfrastructure() Infrastructure {
	return Infrastructure{
		Logger:             slog.Default(),
		EventBus:           eventbus.New(),
		HTTPClient:         httpx.NewHTTPClient(10*time.Second, nil),
		Background:         func(_ string, _ func(ctx context.Context) error) {},
		SetCustomReadiness: func(_ http.Handler) {},
		AddHealthCheck:     func(_ health.DependencyCheck) {},
	}
}
