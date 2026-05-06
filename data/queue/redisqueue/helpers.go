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

// removeByIDScript scans the processing list for an entry whose JSON has the
// given message ID, then atomically replaces it with a tombstone and LREMs
// the tombstone. This avoids LREM-by-payload races where two messages with
// identical bytes (rare but possible — especially on retry of identical
// user-enqueued payloads) would result in the wrong copy being removed.
//
// The scan is O(n) over the per-consumer processing list, which is bounded
// by the consumer's in-flight count (typically <100). The cost is amortised
// over a successful handler invocation.
//
//	KEYS[1] = processing list
//	ARGV[1] = message ID to remove
var removeByIDScript = goredis.NewScript(`
local items = redis.call('LRANGE', KEYS[1], 0, -1)
local needle = '"id":"' .. ARGV[1] .. '"'
for i = 1, #items do
	if string.find(items[i], needle, 1, true) then
		local sentinel = '__rho-tombstone__:' .. KEYS[1] .. ':' .. ARGV[1] .. ':' .. tostring(i)
		redis.call('LSET', KEYS[1], i - 1, sentinel)
		redis.call('LREM', KEYS[1], 1, sentinel)
		return 1
	end
end
return 0
`)

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

// removeByID runs the Lua tombstone script. Returns nil if the entry was
// removed or no longer present (both treated as success).
func (q *Queue) removeByID(ctx context.Context, processingQ, msgID string) error {
	if _, err := removeByIDScript.Run(ctx, q.client, []string{processingQ}, msgID).Result(); err != nil && !errors.Is(err, goredis.Nil) {
		return err
	}
	return nil
}

// handleMessage processes a single queue message: unmarshal, handle, ack.
// The processing-list entry is removed by message ID after the message is
// either successfully handled or routed to retry/dead-letter. If post-
// handler routing fails, the entry is left in the processing list and will
// be retried on the next recovery scan.
func (q *Queue) handleMessage(ctx context.Context, data string, processingQ, queue, deadQ string, handler Handler) {
	var msg Message
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		q.logger.Error("failed to unmarshal queue message, discarding",
			"error", err,
		)
		// Use a background context — malformed messages must be removed even
		// during shutdown. The parent ctx may already be cancelled, which would
		// make LRem fail immediately and leave garbage in the processing queue.
		// We don't have an ID to use removeByID here, so fall back to literal
		// LRem — for a malformed payload, payload-equality is the only handle
		// we have.
		ackCtx, ackCancel := context.WithTimeout(context.Background(), queueAckTimeout)
		defer ackCancel()
		if remErr := q.client.LRem(ackCtx, processingQ, 1, data).Err(); remErr != nil {
			q.logger.Error("failed to remove malformed message from processing queue",
				"error", remErr,
			)
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
			return // Message left in processing queue — recover on next pass.
		}
	} else {
		q.metrics.messagesProcessed.WithLabelValues(queue).Inc()
	}

	// Remove from processing queue only after successful dispatch to
	// the next destination (success ACK, retry re-enqueue, or dead-letter).
	//
	// Note: there is a small crash window between the dispatch (RPUSH/LPUSH)
	// and this removal. If the process crashes in that window, the message
	// will exist in both queues and be reprocessed on recovery. This is why
	// handlers MUST be idempotent (see Process godoc and doc.go).
	if remErr := q.removeByID(ackCtx, processingQ, msg.ID); remErr != nil {
		q.logger.Error("failed to remove message from processing queue",
			"queue", queue,
			"msg_id", msg.ID,
			"error", remErr,
		)
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

// recoverProcessingBatchSize bounds how many entries one recoverProcessing
// pass scans. Kept small so the interleaved recovery loop in processOnce
// can stay fair to new-message traffic — a permanent N-entry backlog gets
// cleared in N/recoverProcessingBatchSize passes amortised across normal
// reads, instead of head-of-line-blocking the queue at every restart.
const recoverProcessingBatchSize = 10

// recoverProcessing replays up to [recoverProcessingBatchSize] messages
// left in THIS consumer's processing list (e.g. from a previous crash of
// this consumer ID) through the normal handleMessage path.
//
// The previous design RPop'd before dispatch, which silently dropped
// messages whose dispatch failed (handleFailedMessage returned false). The
// current design snapshots the per-consumer list with LRange and feeds each
// entry through handleMessage, which removes by message ID after a
// successful dispatch (or leaves it in place for the next recovery pass on
// dispatch failure).
//
// Returns the number of recovery items processed (so processOnce can
// decide whether to keep interleaving or fall through to the BLMove loop).
//
// WARNING: Even with per-consumer scoping, there is a brief double-process
// window between handler dispatch and removeByID. Handlers MUST be
// idempotent.
func (q *Queue) recoverProcessing(ctx context.Context, processingQ, queue, deadQ string, handler Handler) (int, error) {
	// LRange of the per-consumer list: BLMOVE pushes new claims to the LEFT,
	// so the oldest claims sit at the tail. Pull up to recoverProcessingBatchSize
	// from the tail and process oldest-first to mirror FIFO ordering.
	items, err := q.client.LRange(ctx, processingQ, -recoverProcessingBatchSize, -1).Result()
	if err != nil {
		return 0, fmt.Errorf("lrange processing: %w", err)
	}
	if len(items) == 0 {
		return 0, nil
	}
	q.logger.Info("recovering processing list",
		"queue", queue,
		"consumer_id", q.consumerID,
		"count", len(items),
	)

	for _, data := range items {
		if ctx.Err() != nil {
			return 0, nil
		}
		q.handleMessage(ctx, data, processingQ, queue, deadQ, handler)
	}

	return len(items), nil
}
