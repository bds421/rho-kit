package app

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"

	"google.golang.org/grpc"
	"gorm.io/gorm"

	mwrl "github.com/bds421/rho-kit/httpx/middleware/ratelimit"
	"github.com/bds421/rho-kit/infra/messaging"
	kitredis "github.com/bds421/rho-kit/infra/redis"
	"github.com/bds421/rho-kit/infra/storage"
	"github.com/bds421/rho-kit/observability/auditlog"
	"github.com/bds421/rho-kit/observability/health"
	kitcron "github.com/bds421/rho-kit/runtime/cron"
	"github.com/bds421/rho-kit/runtime/eventbus"
	"github.com/bds421/rho-kit/security/jwtutil"
)

// RouterFunc builds the service's HTTP handler from the initialized infrastructure.
// It is called after all With*() infrastructure is set up but before the server starts.
type RouterFunc func(infra Infrastructure) http.Handler

// SeedFunc is called when the --seed flag is present. It receives the DB and seed
// file path. If it returns nil, the process exits cleanly after seeding.
type SeedFunc func(db *gorm.DB, seedPath string, logger *slog.Logger) error

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

	DB        *gorm.DB                   // nil if no WithMySQL or WithPostgres
	Broker    messaging.Connector        // nil if no WithRabbitMQ
	Publisher messaging.MessagePublisher // nil if no WithRabbitMQ
	Consumer  messaging.MessageConsumer  // nil if no WithRabbitMQ

	JWT *jwtutil.Provider // nil if no WithJWT

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
		HTTPClient:         &http.Client{},
		Background:         func(_ string, _ func(ctx context.Context) error) {},
		SetCustomReadiness: func(_ http.Handler) {},
		AddHealthCheck:     func(_ health.DependencyCheck) {},
	}
}
