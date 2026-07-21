package redisqueue

import (
	"context"

	"github.com/bds421/rho-kit/observability/v2/health"
)

// DepthCheck returns a health.DependencyCheck that monitors the queue depth.
// When the depth exceeds threshold, the check reports StatusDegraded.
// The check is non-critical by default — set DependencyCheck.Critical = true
// after creation to make queue overflow a readiness failure.
//
// Depth is [Queue.Len]: asynq Pending + Retry (not Scheduled). A systemic
// handler failure that piles work into Retry is therefore visible to
// readiness. Each evaluation calls Inspector.GetQueueInfo via Len, which
// is cancelled when the health probe context ends.
func (q *Queue) DepthCheck(queueName string, threshold int64) health.DependencyCheck {
	return health.DependencyCheck{
		Name: health.OpaqueCheckName("queue-depth", queueName),
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
