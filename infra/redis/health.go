package redis

import (
	"context"

	"github.com/bds421/rho-kit/observability/health"
)

// HealthCheck returns a critical DependencyCheck for Redis.
// When the connection has never been established (lazy connect in progress),
// it reports StatusConnecting. After a successful connection, unhealthy state
// reports StatusUnhealthy.
//
// Status logic:
//   - "healthy"    — connection is active
//   - "connecting" — never connected yet (lazy connect still in progress)
//   - "unhealthy"  — was previously connected but is now unreachable
func HealthCheck(conn *Connection) health.DependencyCheck {
	return health.DependencyCheck{
		Name: "redis",
		Check: func(_ context.Context) string {
			if conn.Healthy() {
				return health.StatusHealthy
			}
			if !conn.WasConnected() {
				return health.StatusConnecting
			}
			return health.StatusUnhealthy
		},
		Critical: true,
	}
}

// NonCriticalHealthCheck returns a non-critical DependencyCheck for Redis.
// An unhealthy Redis causes degraded status but does not trigger HTTP 503.
// Use for services that can partially function without Redis (e.g. cache-only usage).
func NonCriticalHealthCheck(conn *Connection) health.DependencyCheck {
	dc := HealthCheck(conn)
	dc.Critical = false
	return dc
}

// CriticalHealthCheck is an alias for [HealthCheck] (critical by default).
// Provided for symmetry with [NonCriticalHealthCheck].
func CriticalHealthCheck(conn *Connection) health.DependencyCheck {
	return HealthCheck(conn)
}
