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

// removeByIDScript scans the processing list for an entry whose top-level
// JSON `id` field equals the given message ID, then atomically replaces it
// with a tombstone and LREMs the tombstone. This avoids LREM-by-payload
// races where two messages with identical bytes would result in the wrong
// copy being removed.
//
// We decode each entry with cjson.decode and compare the decoded `id`
// field exactly. The earlier "find a substring like '\"id\":\"X\"'"
// approach is unsafe: an ID containing a JSON-escaped quote or backslash
// produces a needle that no entry contains, so ack silently misses; a
// payload field that happens to spell the literal "id":"<other>" also
// matches and removes the wrong entry. cjson decoding is robust to both
// escaping and to nested `id` fields.
//
// The scan is O(n) over the per-consumer processing list, which is bounded
// by the consumer's in-flight count (typically <100). Decode cost per
// entry is paid only on the success path.
//
//	KEYS[1] = processing list
//	ARGV[1] = message ID to remove (compared against decoded top-level "id")
var removeByIDScript = goredis.NewScript(`
local items = redis.call('LRANGE', KEYS[1], 0, -1)
for i = 1, #items do
	local ok, decoded = pcall(cjson.decode, items[i])
	if ok and type(decoded) == 'table' and decoded.id == ARGV[1] then
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

// removeByID runs the Lua tombstone script. Returns (true, nil) when the
// entry was removed, (false, nil) when no entry matched the ID (the
// processing list did not contain the message — a corruption signal that
// the caller surfaces via metric + warn log), or (false, err) on a Redis-
// level execution error.
func (q *Queue) removeByID(ctx context.Context, processingQ, msgID string) (bool, error) {
	res, err := removeByIDScript.Run(ctx, q.client, []string{processingQ}, msgID).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return false, nil
		}
		return false, err
	}
	n, ok := res.(int64)
	if !ok {
		return false, fmt.Errorf("removeByID: unexpected script result type %T", res)
	}
	return n == 1, nil
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
	removed, remErr := q.removeByID(ackCtx, processingQ, msg.ID)
	if remErr != nil {
		q.logger.Error("failed to remove message from processing queue",
			"queue", queue,
			"msg_id", msg.ID,
			"error", remErr,
		)
		return
	}
	if !removed {
		q.metrics.ackNotFound.WithLabelValues(queue).Inc()
		q.logger.Warn("ack found no matching processing-list entry — possible corruption or concurrent reap",
			"queue", queue,
			"msg_id", msg.ID,
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

// defaultReapInitialDelay is how long the reaper waits before its first
// pass so freshly-started peers have time to write their heartbeats.
// Without this delay, a fast-starting reaper could observe a peer that has
// already started Process but hasn't yet written its heartbeat, and
// reclaim the peer's in-flight messages (causing duplicate processing).
const defaultReapInitialDelay = 5 * time.Second

// reaperScanCount bounds how many keys are returned per SCAN cursor pass.
// Conservative to avoid blocking Redis on large key spaces.
const reaperScanCount int64 = 256

// reapBatchSize bounds how many entries are reclaimed from a dead
// consumer's processing list per reaper pass. Bounded so a backlog of
// stranded entries does not produce a single oversized RPUSH burst.
const reapBatchSize int64 = 100

// heartbeatRefreshTimeout caps each individual SET/EXPIRE call so a
// transient Redis stall in the heartbeat goroutine cannot block shutdown.
const heartbeatRefreshTimeout = 5 * time.Second

// refreshHeartbeat writes the heartbeat key with the configured TTL.
// Errors are logged but not propagated — heartbeat misses are recoverable
// (the peer reaper will rediscover us on the next refresh).
func (q *Queue) refreshHeartbeat(ctx context.Context, heartbeatKey string) {
	hbCtx, cancel := context.WithTimeout(ctx, heartbeatRefreshTimeout)
	defer cancel()
	if err := q.client.Set(hbCtx, heartbeatKey, "1", q.heartbeatTTL).Err(); err != nil {
		// During shutdown the parent ctx may be cancelled; treat as benign.
		if ctx.Err() != nil {
			return
		}
		q.logger.Warn("failed to refresh heartbeat",
			"consumer_id", q.consumerID,
			"error", err,
		)
	}
}

// heartbeatLoop refreshes the heartbeat key at heartbeatInterval. Stops on
// ctx cancellation. Does NOT delete the key on shutdown — the TTL handles
// that, and leaving the key alive briefly past shutdown is the correct
// posture: it gives any in-flight processing-list entries to be re-popped
// on restart by the same consumer ID (when WithConsumerID is used).
func (q *Queue) heartbeatLoop(ctx context.Context, heartbeatKey string) {
	ticker := time.NewTicker(q.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q.refreshHeartbeat(ctx, heartbeatKey)
		}
	}
}

// reaperLoop periodically scans for processing lists owned by dead
// consumers (heartbeat key missing) and reclaims their entries. Stops on
// ctx cancellation. The cadence is tied to the heartbeat TTL: we wait at
// least one TTL between passes so a peer that briefly stalled doesn't get
// erroneously reclaimed.
func (q *Queue) reaperLoop(
	ctx context.Context,
	queue, heartbeatPrefix, processingPrefix, ownProcessingQ, deadQ string,
	handler Handler,
) {
	initialDelay := q.reapInitialDelay
	if initialDelay <= 0 {
		initialDelay = defaultReapInitialDelay
	}
	// Initial delay: let any fresh peers write heartbeats first.
	select {
	case <-ctx.Done():
		return
	case <-time.After(initialDelay):
	}

	q.reapDeadConsumers(ctx, queue, heartbeatPrefix, processingPrefix, ownProcessingQ, deadQ, handler)

	ticker := time.NewTicker(q.heartbeatTTL)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q.reapDeadConsumers(ctx, queue, heartbeatPrefix, processingPrefix, ownProcessingQ, deadQ, handler)
		}
	}
}

// reapDeadConsumers scans for stranded processing lists and reclaims them.
// A processing list is "stranded" when its consumer ID has no live
// heartbeat key. Reclaim is idempotent — entries are RPUSHed onto the main
// queue and then LREMed from the dead list one at a time, so a crash mid-
// reclaim leaves at most one duplicate (and handlers MUST be idempotent).
func (q *Queue) reapDeadConsumers(
	ctx context.Context,
	queue, heartbeatPrefix, processingPrefix, ownProcessingQ, deadQ string,
	handler Handler,
) {
	processingPattern := processingPrefix + "*"
	var cursor uint64
	for {
		if ctx.Err() != nil {
			return
		}
		keys, next, err := q.client.Scan(ctx, cursor, processingPattern, reaperScanCount).Result()
		if err != nil {
			if ctx.Err() == nil {
				q.logger.Warn("processing-list scan failed",
					"queue", queue,
					"error", err,
				)
			}
			return
		}
		for _, key := range keys {
			if key == ownProcessingQ {
				continue
			}
			deadConsumerID := key[len(processingPrefix):]
			if deadConsumerID == "" {
				continue
			}
			heartbeatKey := heartbeatPrefix + deadConsumerID
			alive, err := q.client.Exists(ctx, heartbeatKey).Result()
			if err != nil {
				if ctx.Err() == nil {
					q.logger.Warn("heartbeat existence check failed",
						"queue", queue,
						"dead_consumer_id", deadConsumerID,
						"error", err,
					)
				}
				continue
			}
			if alive > 0 {
				continue
			}
			q.reclaimProcessingList(ctx, queue, key, deadConsumerID)
		}
		if next == 0 {
			return
		}
		cursor = next
	}
}

// reclaimProcessingList moves entries from a dead consumer's processing
// list back to the main queue tail and deletes the list. Bounded by
// reapBatchSize per pass so one giant stranded list cannot dominate a
// single pass. Called only after the heartbeat has been verified missing.
func (q *Queue) reclaimProcessingList(ctx context.Context, queue, deadProcessingQ, deadConsumerID string) {
	for {
		if ctx.Err() != nil {
			return
		}
		items, err := q.client.LRange(ctx, deadProcessingQ, -reapBatchSize, -1).Result()
		if err != nil {
			if ctx.Err() == nil {
				q.logger.Warn("dead-list LRange failed",
					"queue", queue,
					"dead_consumer_id", deadConsumerID,
					"error", err,
				)
			}
			return
		}
		if len(items) == 0 {
			// List is empty — delete it so it doesn't reappear in the next scan.
			if err := q.client.Del(ctx, deadProcessingQ).Err(); err != nil && ctx.Err() == nil {
				q.logger.Warn("dead-list cleanup Del failed",
					"queue", queue,
					"dead_consumer_id", deadConsumerID,
					"error", err,
				)
			}
			return
		}

		q.logger.Info("reclaiming entries from dead consumer's processing list",
			"queue", queue,
			"dead_consumer_id", deadConsumerID,
			"count", len(items),
		)

		// Process oldest first (tail-of-list = earliest-claimed), one at a
		// time, so each entry's RPUSH-then-LREM is sequential. Avoids a
		// pipeline that could leave the queue with N duplicates if one
		// command failed mid-pipeline.
		for i := len(items) - 1; i >= 0; i-- {
			if ctx.Err() != nil {
				return
			}
			data := items[i]
			if err := q.client.RPush(ctx, queue, data).Err(); err != nil {
				if ctx.Err() == nil {
					q.logger.Warn("RPush to main queue failed during reclaim",
						"queue", queue,
						"dead_consumer_id", deadConsumerID,
						"error", err,
					)
				}
				return
			}
			if err := q.client.LRem(ctx, deadProcessingQ, 1, data).Err(); err != nil && ctx.Err() == nil {
				q.logger.Warn("LRem from dead list failed during reclaim",
					"queue", queue,
					"dead_consumer_id", deadConsumerID,
					"error", err,
				)
				// Don't return — main queue already has the entry; continue
				// so we don't re-RPUSH the same data infinitely.
			}
		}
	}
}

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
