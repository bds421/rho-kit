package redisqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/apperror"
)

func (q *Queue) updateProcessingDepth(ctx context.Context, queue, processingQ string) {
	if n, err := q.client.LLen(ctx, processingQ).Result(); err == nil {
		q.metrics.processingDepth.WithLabelValues(queue).Set(float64(n))
	}
	if n, err := q.client.LLen(ctx, queue).Result(); err == nil {
		q.metrics.queueDepth.WithLabelValues(queue).Set(float64(n))
	}
}

const (
	// queueAckTimeout is the maximum time for post-handler operations (LRem,
	// dead-letter LPush) which must succeed even if the handler cancelled ctx.
	queueAckTimeout = 10 * time.Second

	// queueHandlerShutdownTimeout is the grace period given to a queue handler
	// that is still running when the parent context is cancelled.
	queueHandlerShutdownTimeout = 30 * time.Second
)

// handleMessage processes a single queue message: unmarshal, handle, ack.
// If alreadyRemoved is true, the message was already removed from the processing
// queue (e.g., by RPop during crash recovery) and LRem is skipped to avoid
// removing a different message with identical data.
func (q *Queue) handleMessage(ctx context.Context, data string, processingQ, queue, deadQ string, handler Handler, alreadyRemoved bool) {
	var msg Message
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		q.logger.Error("failed to unmarshal queue message, discarding",
			"error", err,
		)
		if !alreadyRemoved {
			// Use a background context — malformed messages must be removed even
			// during shutdown. The parent ctx may already be cancelled, which would
			// make LRem fail immediately and leave garbage in the processing queue.
			ackCtx, ackCancel := context.WithTimeout(context.Background(), queueAckTimeout)
			defer ackCancel()
			if remErr := q.client.LRem(ackCtx, processingQ, 1, data).Err(); remErr != nil {
				q.logger.Error("failed to remove malformed message from processing queue",
					"error", remErr,
				)
			}
		}
		q.metrics.messagesFailed.WithLabelValues(queue).Inc()
		return
	}

	// Give the handler a bounded context. On the normal path, this provides an
	// upper bound on handler execution time (matching stream consumer behavior).
	// On shutdown, a fresh context with the same timeout is used so the handler
	// can complete even after the parent context is cancelled.
	var handlerCtx context.Context
	var handlerCancel context.CancelFunc
	if ctx.Err() != nil {
		handlerCtx, handlerCancel = context.WithTimeout(context.Background(), queueHandlerShutdownTimeout)
	} else {
		handlerCtx, handlerCancel = context.WithTimeout(ctx, queueHandlerShutdownTimeout)
	}
	defer handlerCancel()

	start := time.Now()
	handlerErr := handler(handlerCtx, msg)
	duration := time.Since(start)
	q.metrics.processingDuration.WithLabelValues(queue).Observe(duration.Seconds())

	// Use a fresh context for post-handler operations. The handler may have
	// cancelled the parent context, but acknowledgment must still complete.
	ackCtx, ackCancel := context.WithTimeout(context.Background(), queueAckTimeout)
	defer ackCancel()

	if handlerErr != nil {
		q.metrics.messagesFailed.WithLabelValues(queue).Inc()
		if !q.handleFailedMessage(ackCtx, queue, deadQ, data, msg, handlerErr) {
			return // Message left in processing queue — recover on restart.
		}
	} else {
		q.metrics.messagesProcessed.WithLabelValues(queue).Inc()
	}

	// Remove from processing queue only after successful dispatch to
	// the next destination (success ACK, retry re-enqueue, or dead-letter).
	//
	// Note: there is a small crash window between the dispatch (RPUSH/LPUSH)
	// and this LRem. If the process crashes in that window, the message will
	// exist in both queues and be reprocessed on recovery. This is why handlers
	// MUST be idempotent (see Process godoc and doc.go).
	if !alreadyRemoved {
		if remErr := q.client.LRem(ackCtx, processingQ, 1, data).Err(); remErr != nil {
			q.logger.Error("failed to remove message from processing queue",
				"queue", queue,
				"msg_id", msg.ID,
				"error", remErr,
			)
		}
	}
}

// handleFailedMessage routes a failed message to dead-letter or retry.
// Returns true if the message was successfully routed (and can be removed from
// the processing queue), false if it should be left for crash recovery.
// Permanent errors (apperror.PermanentError) are immediately dead-lettered
// without further retries, matching the stream consumer's behavior.
func (q *Queue) handleFailedMessage(ctx context.Context, queue, deadQ, data string, msg Message, handlerErr error) bool {
	if apperror.IsPermanent(handlerErr) {
		q.logger.Error("permanent error, dead-lettering message",
			"queue", queue,
			"msg_id", msg.ID,
			"error", handlerErr,
		)
		return q.deadLetter(ctx, queue, deadQ, data, msg.ID)
	}

	if msg.Attempt >= q.maxRetries {
		q.logger.Error("max retries exceeded, dead-lettering message",
			"queue", queue,
			"msg_id", msg.ID,
			"attempt", msg.Attempt,
			"error", handlerErr,
		)
		return q.deadLetter(ctx, queue, deadQ, data, msg.ID)
	}

	// Re-enqueue to the TAIL of the queue (RPUSH) so other messages are
	// processed before the retry, providing natural backoff. This prevents
	// tight retry loops when a message consistently fails — there will always
	// be at least a full queue cycle between attempts.
	//
	// Immutable — creates a new struct with incremented attempt count.
	retryMsg := Message{
		ID:        msg.ID,
		Type:      msg.Type,
		Payload:   msg.Payload,
		Timestamp: msg.Timestamp,
		Attempt:   msg.Attempt + 1,
	}
	retryData, marshalErr := json.Marshal(retryMsg)
	if marshalErr != nil {
		q.logger.Error("failed to marshal retry message, leaving in processing queue",
			"msg_id", msg.ID,
			"error", marshalErr,
		)
		return false
	}
	if retryErr := q.client.RPush(ctx, queue, retryData).Err(); retryErr != nil {
		q.logger.Error("failed to re-enqueue message, leaving in processing queue",
			"queue", queue,
			"msg_id", msg.ID,
			"error", retryErr,
		)
		return false
	}
	q.metrics.messagesRetried.WithLabelValues(queue).Inc()
	return true
}

// deadLetter atomically pushes data to the dead-letter queue and trims it.
// Uses a pipeline for single-roundtrip atomicity, preventing concurrent
// dead-lettering from silently dropping messages between LPush and LTrim.
func (q *Queue) deadLetter(ctx context.Context, queue, deadQ, data, msgID string) bool {
	pipe := q.client.Pipeline()
	pipe.LPush(ctx, deadQ, data)
	if q.deadLetterMax > 0 {
		pipe.LTrim(ctx, deadQ, 0, q.deadLetterMax-1)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		q.logger.Error("failed to dead-letter message, leaving in processing queue",
			"queue", queue,
			"msg_id", msgID,
			"error", err,
		)
		return false
	}
	q.metrics.messagesDeadLettered.WithLabelValues(queue).Inc()
	return true
}

// recoverProcessing moves messages from the processing queue back to the
// main queue or dead-letters them if max retries exceeded.
//
// WARNING: During recovery there is a double-processing window where a message
// may be handled while a previously crashed goroutine's partial processing is
// still in flight. Handlers MUST be idempotent to handle this correctly.
func (q *Queue) recoverProcessing(ctx context.Context, processingQ, queue, deadQ string, handler Handler) error {
	recovered := 0
	for {
		if ctx.Err() != nil {
			return nil
		}

		data, err := q.client.RPop(ctx, processingQ).Result()
		if err != nil {
			if errors.Is(err, goredis.Nil) {
				if recovered > 0 {
					q.logger.Info("queue crash recovery complete",
						"queue", queue,
						"recovered", recovered,
					)
				}
				return nil // no more messages to recover
			}
			return fmt.Errorf("rpop processing: %w", err)
		}

		recovered++
		q.handleMessage(ctx, data, processingQ, queue, deadQ, handler, true)
	}
}
