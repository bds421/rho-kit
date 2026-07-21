package sqldb

import (
	"context"

	"github.com/bds421/rho-kit/observability/v2/health"
)

// Pinger checks database connectivity without depending on a specific driver.
// Implementations should apply a short timeout internally because the
// context-less [Pinger.Ping] cannot observe the health framework's
// cooperative cancellation. *sql.DB satisfies this interface.
//
// Prefer [ContextPinger] (and [HealthCheckContext]) where possible so the
// framework's cancellation context is threaded through the ping; otherwise a
// hung ping keeps holding a DB connection after the kubelet probe has given
// up. *sql.DB satisfies [ContextPinger] via PingContext. The pgx adapter
// (infra/sqldb/pgx.Pool) implements PingContext directly.
type Pinger interface {
	Ping() error
}

// ContextPinger checks database connectivity while honoring a caller-supplied
// context. Implementations must thread ctx through the underlying ping so the
// health framework's cooperative timeout can release the DB connection when a
// readiness probe is cancelled. *sql.DB satisfies this via PingContext.
type ContextPinger interface {
	PingContext(ctx context.Context) error
}

// HealthCheck returns a critical [health.DependencyCheck] that pings the database.
//
// If pinger also implements [ContextPinger], the framework's cancellation
// context is threaded through PingContext so a hung ping does not keep holding
// a DB connection after a readiness probe is cancelled; otherwise the
// context-less [Pinger.Ping] is used. Prefer [HealthCheckContext] when the
// pinger is statically known to be context-aware.
//
// Panics if pinger is nil — a nil pinger would panic on every health
// scrape, leaking the wiring bug as a nil-deref in operator logs.
// Wave 68 closed a hostile-review finding for the panic-at-runtime
// surface.
func HealthCheck(pinger Pinger) health.DependencyCheck {
	if pinger == nil {
		panic("sqldb: HealthCheck requires a non-nil Pinger")
	}
	if cp, ok := pinger.(ContextPinger); ok {
		return healthCheck(cp.PingContext)
	}
	return healthCheck(func(context.Context) error { return pinger.Ping() })
}

// HealthCheckContext returns a critical [health.DependencyCheck] that pings the
// database using a [ContextPinger], threading the health framework's
// cooperative-cancellation context through every ping. Use this when the pinger
// is statically known to be context-aware (e.g. *sql.DB via PingContext, or the
// pgx adapter wrapped in a small PingContext shim around its Ping(ctx) method)
// so a hung ping releases its DB connection once the readiness probe is
// cancelled.
//
// Panics if pinger is nil, matching [HealthCheck].
func HealthCheckContext(pinger ContextPinger) health.DependencyCheck {
	if pinger == nil {
		panic("sqldb: HealthCheckContext requires a non-nil ContextPinger")
	}
	return healthCheck(pinger.PingContext)
}

func healthCheck(ping func(context.Context) error) health.DependencyCheck {
	return health.DependencyCheck{
		Name: "database",
		Check: func(ctx context.Context) string {
			if err := ping(ctx); err != nil {
				return health.StatusUnhealthy
			}
			return health.StatusHealthy
		},
		Critical: true,
	}
}
