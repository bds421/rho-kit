package sqldb

import (
	"context"

	"github.com/bds421/rho-kit/observability/v2/health"
)

// Pinger checks database connectivity without depending on a specific driver.
// Implementations should apply a short timeout internally. The pgx adapter
// (infra/sqldb/pgx) satisfies this interface; *sql.DB does too.
type Pinger interface {
	Ping() error
}

// HealthCheck returns a critical [health.DependencyCheck] that pings the database.
//
// Panics if pinger is nil — a nil pinger would panic on every health
// scrape, leaking the wiring bug as a nil-deref in operator logs.
// Wave 68 closed a hostile-review finding for the panic-at-runtime
// surface.
func HealthCheck(pinger Pinger) health.DependencyCheck {
	if pinger == nil {
		panic("sqldb: HealthCheck requires a non-nil Pinger")
	}
	return health.DependencyCheck{
		Name: "database",
		Check: func(_ context.Context) string {
			if err := pinger.Ping(); err != nil {
				return health.StatusUnhealthy
			}
			return health.StatusHealthy
		},
		Critical: true,
	}
}
