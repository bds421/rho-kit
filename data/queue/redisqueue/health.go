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
// Each evaluation calls [Queue.Len], which delegates to asynq's
// Inspector.GetQueueInfo (a single Lua EVAL round-trip on the queue's
// pending-state key); cost is comparable to the pre-v2 LLEN poll.
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
