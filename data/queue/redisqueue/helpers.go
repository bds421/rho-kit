package redisqueue

import (
	"context"
	"fmt"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// queueEnvelopeOverhead is the assumed JSON envelope size (headers, type,
// id, timestamp, structural bytes) added to Message.Payload when computing
// the pre-unmarshal length cap. 8 KiB comfortably exceeds the per-field
// caps imposed by [validateMessage] without permitting a parse-cost DoS.
const queueEnvelopeOverhead = 8 * 1024

// callHandler invokes handler with a defensive panic recover so a panicking
// handler doesn't crash the asynq worker pool. The returned error matches
// the kit's redact convention so panic values never reach logs verbatim.
func callHandler(ctx context.Context, handler Handler, msg Message) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("redisqueue: handler panic: %s", redact.PanicValue(rec))
		}
	}()
	return handler(ctx, msg.Clone())
}

// updateProcessingDepth polls asynq's Inspector for the named queue and
// updates the kit's queue_depth, processing_depth, and dlq_depth gauges in
// a single pass. Errors are intentionally swallowed — depth is a
// best-effort signal and the surrounding poller already runs at a fixed
// cadence.
func (q *Queue) updateProcessingDepth(ctx context.Context, queue string) {
	if err := ctx.Err(); err != nil {
		return
	}
	info, err := q.inspector.GetQueueInfo(queue)
	if err != nil {
		if isQueueNotFoundError(err) {
			// Queue has not yet been created — leave gauges at zero.
			label := queueMetricLabel(queue)
			q.metrics.queueDepth.WithLabelValues(label).Set(0)
			q.metrics.processingDepth.WithLabelValues(label).Set(0)
			q.metrics.dlqDepth.WithLabelValues(label).Set(0)
			return
		}
		// Other inspector errors (transient Redis blip, auth failure)
		// surface in the kit log but do not block the poller.
		q.logger.Debug("asynq inspector depth poll failed",
			redact.String("queue", queue),
			redact.Error(err),
		)
		return
	}
	label := queueMetricLabel(queue)
	q.metrics.queueDepth.WithLabelValues(label).Set(float64(info.Pending))
	q.metrics.processingDepth.WithLabelValues(label).Set(float64(info.Active))
	q.metrics.dlqDepth.WithLabelValues(label).Set(float64(info.Archived))
}

// startDepthPoller runs updateProcessingDepth on a fixed ticker for the
// lifetime of the returned stop function. The poller is started by
// [Queue.Process] so the gauges track the same queue the server is
// serving; it terminates when ctx is cancelled OR when the caller invokes
// the returned stop function (whichever comes first).
func (q *Queue) startDepthPoller(ctx context.Context, queue string) func() {
	pollCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// One immediate sample so the gauge isn't zero for the first
		// poll interval after Process starts.
		q.updateProcessingDepth(pollCtx, queue)
		ticker := time.NewTicker(q.healthCheckPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				q.updateProcessingDepth(pollCtx, queue)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
