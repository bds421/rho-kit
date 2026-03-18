package sqldb

import (
	"context"

	"github.com/bds421/rho-kit/observability/health"
)

// Pinger checks database connectivity without depending on a specific driver.
// Implementations should apply a short timeout internally.
// [gormdb.Pinger] satisfies this interface.
type Pinger interface {
	Ping() error
}

// HealthCheck returns a critical [health.DependencyCheck] that pings the database.
func HealthCheck(pinger Pinger) health.DependencyCheck {
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
