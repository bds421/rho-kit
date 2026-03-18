package amqpbackend

import (
	"context"

	"github.com/bds421/rho-kit/observability/health"
)

// HealthCheck returns a non-critical [health.DependencyCheck] for the AMQP broker.
// Returns "connecting" when the broker is unreachable (lazy connect may still
// be in progress) rather than "unhealthy" to avoid false 503s during startup.
func HealthCheck(conn *Connection) health.DependencyCheck {
	return health.DependencyCheck{
		Name: "rabbitmq",
		Check: func(_ context.Context) string {
			if !conn.Healthy() {
				return health.StatusConnecting
			}
			return health.StatusHealthy
		},
	}
}

// CriticalHealthCheck returns a critical [health.DependencyCheck] for the AMQP broker.
// An unhealthy broker triggers HTTP 503 on the readiness endpoint.
// Use for services that cannot function without the broker (e.g. notification-service).
func CriticalHealthCheck(conn *Connection) health.DependencyCheck {
	return health.DependencyCheck{
		Name: "rabbitmq",
		Check: func(_ context.Context) string {
			if !conn.Healthy() {
				return health.StatusUnhealthy
			}
			return health.StatusHealthy
		},
		Critical: true,
	}
}
