package sftpbackend

import (
	"context"
	"strconv"

	"github.com/bds421/rho-kit/observability/v2/health"
)

// HealthCheck returns a non-critical DependencyCheck for SFTP.
// It stats the root path as a lightweight connectivity probe.
func HealthCheck(b *Backend) health.DependencyCheck {
	return healthCheck(b, false)
}

// CriticalHealthCheck returns a critical DependencyCheck for SFTP.
// An unhealthy SFTP triggers HTTP 503 on the readiness endpoint.
func CriticalHealthCheck(b *Backend) health.DependencyCheck {
	return healthCheck(b, true)
}

func healthCheck(b *Backend, critical bool) health.DependencyCheck {
	if b == nil {
		panic("sftpbackend: HealthCheck requires a non-nil Backend")
	}
	return health.DependencyCheck{
		Name: health.OpaqueCheckName("sftp", b.cfg.Host, strconv.Itoa(b.cfg.Port)),
		Check: func(_ context.Context) string {
			if !b.Healthy() {
				return "unhealthy"
			}
			return "healthy"
		},
		Critical: critical,
	}
}
