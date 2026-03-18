package redisqueue

import (
	"context"
	"fmt"

	"github.com/bds421/rho-kit/observability/health"
)

// DepthCheck returns a health.DependencyCheck that monitors the queue depth.
// When the depth exceeds threshold, the check reports StatusDegraded.
// The check is non-critical by default — set DependencyCheck.Critical = true
// after creation to make queue overflow a readiness failure.
//
// The check issues an LLEN command per evaluation, which is O(1) in Redis.
func (q *Queue) DepthCheck(queueName string, threshold int64) health.DependencyCheck {
	return health.DependencyCheck{
		Name: fmt.Sprintf("queue-%s-depth", queueName),
		Check: func(ctx context.Context) string {
			n, err := q.Len(ctx, queueName)
			if err != nil {
				return health.StatusUnhealthy
			}
			if n > threshold {
				return health.StatusDegraded
			}
			return health.StatusHealthy
		},
	}
}
