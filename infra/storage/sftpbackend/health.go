package sftpbackend

import (
	"context"
	"fmt"

	"github.com/bds421/rho-kit/observability/health"
)

// HealthCheck returns a non-critical DependencyCheck for SFTP.
// It stats the root path as a lightweight connectivity probe.
func HealthCheck(b *SFTPBackend) health.DependencyCheck {
	return healthCheck(b, false)
}

// CriticalHealthCheck returns a critical DependencyCheck for SFTP.
// An unhealthy SFTP triggers HTTP 503 on the readiness endpoint.
func CriticalHealthCheck(b *SFTPBackend) health.DependencyCheck {
	return healthCheck(b, true)
}

func healthCheck(b *SFTPBackend, critical bool) health.DependencyCheck {
	return health.DependencyCheck{
		Name: fmt.Sprintf("sftp:%s:%d", b.cfg.Host, b.cfg.Port),
		Check: func(_ context.Context) string {
			if !b.Healthy() {
				return "unhealthy"
			}
			return "healthy"
		},
		Critical: critical,
	}
}
