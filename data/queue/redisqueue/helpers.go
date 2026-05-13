package redisqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
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

// updateProcessingDepth polls LLEN for the processing list, the main
// queue, and the dead-letter queue, updating all three gauges in a
// single pass. Kept on the existing depth-poller goroutine so a DLQ
// gauge does not pay for an additional poll loop. Errors are
// intentionally swallowed — depth is a best-effort signal and the
// surrounding loop already retries.
func (q *Queue) updateProcessingDepth(ctx context.Context, queue, processingQ, deadQ string) {
	label := queueMetricLabel(queue)
	if n, err := q.client.LLen(ctx, processingQ).Result(); err == nil {
		q.metrics.processingDepth.WithLabelValues(label).Set(float64(n))
	}
	if n, err := q.client.LLen(ctx, queue).Result(); err == nil {
		q.metrics.queueDepth.WithLabelValues(label).Set(float64(n))
	}
	if deadQ != "" {
		if n, err := q.client.LLen(ctx, deadQ).Result(); err == nil {
			q.metrics.dlqDepth.WithLabelValues(label).Set(float64(n))
		}
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

func queueDetachedTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (q *Queue) discardProcessingPayload(ctx context.Context, processingQ, queue, data, reason string, err error) {
	q.logger.Error("discarding invalid queue message",
		redact.String("queue", queue),
		"reason", reason,
		redact.Error(err),
	)
	ackCtx, ackCancel := queueDetachedTimeout(ctx, queueAckTimeout)
	defer ackCancel()
	if remErr := q.client.LRem(ackCtx, processingQ, 1, data).Err(); remErr != nil {
		q.logger.Error("failed to remove invalid message from processing queue",
			redact.String("queue", queue),
			redact.Error(remErr),
		)
	}
	q.metrics.messagesFailed.WithLabelValues(queueMetricLabel(queue)).Inc()
}

// retryAndRemoveScript atomically tombstones the original from the
// processing list AND THEN RPUSHes the retry payload onto the queue.
//
// FR-061 [MED]: pre-fix the script enqueued first and then scanned —
// if a reaper had already moved the original (so the scan failed),
// the retry copy was still on the queue, producing a duplicate.
// The new order proves the original existed (returns 1) before
// enqueueing; on race-with-reaper (returns 0) the retry is NOT
// enqueued and the caller treats the message as already-reclaimed.
//
//	KEYS[1] = queue (re-enqueue target)
//	KEYS[2] = processing list (where original lives)
//	ARGV[1] = retry payload bytes (already attempt+1)
//	ARGV[2] = message ID to tombstone
var retryAndRemoveScript = goredis.NewScript(`
local items = redis.call('LRANGE', KEYS[2], 0, -1)
for i = 1, #items do
	local ok, decoded = pcall(cjson.decode, items[i])
	if ok and type(decoded) == 'table' and decoded.id == ARGV[2] then
		local sentinel = '__rho-tombstone__:' .. KEYS[2] .. ':' .. ARGV[2] .. ':' .. tostring(i)
		redis.call('LSET', KEYS[2], i - 1, sentinel)
		redis.call('LREM', KEYS[2], 1, sentinel)
		redis.call('RPUSH', KEYS[1], ARGV[1])
		return 1
	end
end
return 0
`)

// deadLetterAndRemoveScript atomically tombstones the original from
// the processing list AND THEN LPUSHes the message onto the dead-
// letter queue, applying the LTRIM cap.
//
// FR-061 [MED]: pre-fix the LPUSH happened before the scan, so a
// race with a reaper produced a duplicate dead-letter entry. The
// new order skips the DLQ write when the original is gone.
//
//	KEYS[1] = dead-letter queue
//	KEYS[2] = processing list
//	ARGV[1] = payload bytes
//	ARGV[2] = message ID
//	ARGV[3] = max dead-letter list size (0 to disable LTRIM)
var deadLetterAndRemoveScript = goredis.NewScript(`
local items = redis.call('LRANGE', KEYS[2], 0, -1)
for i = 1, #items do
	local ok, decoded = pcall(cjson.decode, items[i])
	if ok and type(decoded) == 'table' and decoded.id == ARGV[2] then
		local sentinel = '__rho-tombstone__:' .. KEYS[2] .. ':' .. ARGV[2] .. ':' .. tostring(i)
		redis.call('LSET', KEYS[2], i - 1, sentinel)
		redis.call('LREM', KEYS[2], 1, sentinel)
		redis.call('LPUSH', KEYS[1], ARGV[1])
		local maxLen = tonumber(ARGV[3])
		if maxLen and maxLen > 0 then
			redis.call('LTRIM', KEYS[1], 0, maxLen - 1)
		end
		return 1
	end
end
return 0
`)

// handleMessage processes a single queue message: unmarshal, handle, ack.
// The processing-list entry is removed by message ID after the message is
// either successfully handled or routed to retry/dead-letter. If post-
// handler routing fails, the entry is left in the processing list and will
// be retried on the next recovery scan.
func (q *Queue) handleMessage(ctx context.Context, data string, processingQ, queue, deadQ string, handler Handler) {
	var msg Message
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		q.discardProcessingPayload(ctx, processingQ, queue, data, "failed to unmarshal queue message, discarding", err)
		return
	}
	if err := validateMessage(msg, q.maxPayloadSize); err != nil {
		q.discardProcessingPayload(ctx, processingQ, queue, data, "invalid queue message from processing queue, discarding", err)
		return
	}

	// Give the handler a bounded context. On the normal path, this provides an
	// upper bound on handler execution time (matching stream consumer behavior).
	// On shutdown, a fresh context with the same timeout is used so the handler
	// can complete even after the parent context is cancelled.
	var handlerCtx context.Context
	var handlerCancel context.CancelFunc
	if ctx.Err() != nil {
		handlerCtx, handlerCancel = queueDetachedTimeout(ctx, queueHandlerShutdownTimeout)
	} else {
		handlerCtx, handlerCancel = context.WithTimeout(ctx, queueHandlerShutdownTimeout)
	}
	defer handlerCancel()

	start := time.Now()
	handlerErr := callHandler(handlerCtx, handler, msg)
	duration := time.Since(start)
	q.metrics.processingDuration.WithLabelValues(queueMetricLabel(queue)).Observe(duration.Seconds())

	// Use a fresh context for post-handler operations. The handler may have
	// cancelled the parent context, but acknowledgment must still complete.
	ackCtx, ackCancel := queueDetachedTimeout(ctx, queueAckTimeout)
	defer ackCancel()

	if handlerErr != nil {
		q.metrics.messagesFailed.WithLabelValues(queueMetricLabel(queue)).Inc()
		// handleFailedMessage atomically removes from processing — no
		// follow-up removeByID needed here.
		_ = q.handleFailedMessage(ackCtx, queue, deadQ, processingQ, data, msg, handlerErr)
		return
	}
	q.metrics.messagesProcessed.WithLabelValues(queueMetricLabel(queue)).Inc()

	// Remove from processing queue only after successful dispatch.
	removed, remErr := q.removeByID(ackCtx, processingQ, msg.ID)
	if remErr != nil {
		q.logger.Error("failed to remove message from processing queue",
			redact.String("queue", queue),
			redact.String("msg_id", msg.ID),
			redact.Error(remErr),
		)
		return
	}
	if !removed {
		q.metrics.ackNotFound.WithLabelValues(queueMetricLabel(queue)).Inc()
		q.logger.Warn("ack found no matching processing-list entry — possible corruption or concurrent reap",
			redact.String("queue", queue),
			redact.String("msg_id", msg.ID),
		)
	}
}

func callHandler(ctx context.Context, handler Handler, msg Message) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("redisqueue: handler panic: %s", redact.PanicValue(rec))
		}
	}()
	return handler(ctx, msg.Clone())
}

// handleFailedMessage routes a failed message to dead-letter or retry.
// Both paths atomically remove the original from the processing list,
// so the caller MUST NOT also call removeByID — doing so would emit a
// spurious "ack found no matching entry" warning.
//
// Returns true if the message was successfully routed (atomically
// removed from processing), false if it should be left for crash
// recovery (e.g. Redis transient error during the routing call).
//
// Permanent errors (apperror.PermanentError) are immediately
// dead-lettered without further retries, matching the stream consumer's
// behavior.
func (q *Queue) handleFailedMessage(ctx context.Context, queue, deadQ, processingQ, data string, msg Message, handlerErr error) bool {
	if apperror.IsPermanent(handlerErr) {
		q.logger.Error("permanent error, dead-lettering message",
			redact.String("queue", queue),
			redact.String("msg_id", msg.ID),
			redact.Error(handlerErr),
		)
		return q.deadLetterAndRemove(ctx, queue, deadQ, processingQ, data, msg.ID)
	}

	if msg.Attempt >= q.maxRetries {
		q.logger.Error("max retries exceeded, dead-lettering message",
			redact.String("queue", queue),
			redact.String("msg_id", msg.ID),
			"attempt", msg.Attempt,
			redact.Error(handlerErr),
		)
		return q.deadLetterAndRemove(ctx, queue, deadQ, processingQ, data, msg.ID)
	}

	// Re-enqueue to the TAIL of the queue (RPUSH) so other messages are
	// processed before the retry, providing natural backoff. RPUSH and
	// the tombstone-of-the-original are committed atomically by the Lua
	// script, eliminating the duplicate-processing window the previous
	// non-atomic Go-side sequence had under cancelled-ctx races.
	//
	retryMsg := msg.Clone()
	retryMsg.Attempt++
	retryData, marshalErr := json.Marshal(retryMsg)
	if marshalErr != nil {
		q.logger.Error("failed to marshal retry message, leaving in processing queue",
			redact.String("msg_id", msg.ID),
			redact.Error(marshalErr),
		)
		return false
	}
	res, runErr := retryAndRemoveScript.Run(ctx, q.client, []string{queue, processingQ}, retryData, msg.ID).Result()
	if runErr != nil && !errors.Is(runErr, goredis.Nil) {
		q.logger.Error("retry-and-remove script failed",
			redact.String("queue", queue),
			redact.String("msg_id", msg.ID),
			redact.Error(runErr),
		)
		return false
	}
	// 1 = original tombstoned (normal); 0 = original not found (peer
	// already reclaimed it — still safe because the retry RPUSH ran
	// inside the same atomic script).
	if n, ok := res.(int64); ok && n == 0 {
		q.logger.Warn("retry: original not found in processing list (already reclaimed by peer reaper)",
			redact.String("queue", queue),
			redact.String("msg_id", msg.ID),
		)
	}
	q.metrics.messagesRetried.WithLabelValues(queueMetricLabel(queue)).Inc()
	return true
}

// deadLetterAndRemove atomically dead-letters and tombstones the
// original processing-list entry. Replaces the earlier non-atomic
// pipeline-LPUSH + Go-side LTrim that left a window where a peer
// could re-handle the original between the LPUSH and the eventual
// removeByID.
func (q *Queue) deadLetterAndRemove(ctx context.Context, queue, deadQ, processingQ, data, msgID string) bool {
	res, err := deadLetterAndRemoveScript.Run(
		ctx,
		q.client,
		[]string{deadQ, processingQ},
		data, msgID, q.deadLetterMax,
	).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		q.logger.Error("failed to dead-letter message, leaving in processing queue",
			redact.String("queue", queue),
			redact.String("msg_id", msgID),
			redact.Error(err),
		)
		return false
	}
	if n, ok := res.(int64); ok && n == 0 {
		q.logger.Warn("dead-letter: original not found in processing list (already reclaimed by peer reaper)",
			redact.String("queue", queue),
			redact.String("msg_id", msgID),
		)
	}
	q.metrics.messagesDeadLettered.WithLabelValues(queueMetricLabel(queue)).Inc()
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
// Returns nil on success and the underlying error on failure so the
// caller can decide whether to keep retrying or escalate (for example,
// abort processing so a peer can safely reap our processing list once
// the TTL lapses).
func (q *Queue) refreshHeartbeat(ctx context.Context, heartbeatKey string) error {
	hbCtx, cancel := context.WithTimeout(ctx, heartbeatRefreshTimeout)
	defer cancel()
	if err := q.client.Set(hbCtx, heartbeatKey, "1", q.heartbeatTTL).Err(); err != nil {
		// During shutdown the parent ctx may be cancelled; treat as benign.
		if ctx.Err() != nil {
			return nil
		}
		q.logger.Warn("failed to refresh heartbeat",
			redact.String("consumer_id", q.consumerID),
			redact.Error(err),
		)
		return err
	}
	return nil
}

// maxConsecutiveHeartbeatFailures bounds how many heartbeat refreshes
// may fail in a row before the heartbeat loop stops trying. Once
// crossed, the local consumer is treated as effectively dead so a peer
// reaper can safely reclaim our processing list when the TTL lapses.
const maxConsecutiveHeartbeatFailures = 3

// heartbeatLoop refreshes the heartbeat key at heartbeatInterval. Stops on
// ctx cancellation OR after [maxConsecutiveHeartbeatFailures] consecutive
// errors — at which point the next reaper pass will treat us as dead, so
// we also invoke cancelProcess to stop the local Process loop. Without
// the cancel the local consumer would keep pulling new work from the
// queue while a peer reclaims our processing list, double-dispatching
// messages.
//
// Does NOT delete the heartbeat key on shutdown — the TTL handles that,
// and leaving the key alive briefly past shutdown is the correct posture.
func (q *Queue) heartbeatLoop(ctx context.Context, heartbeatKey string, cancelProcess context.CancelFunc) {
	ticker := time.NewTicker(q.heartbeatInterval)
	defer ticker.Stop()

	consecutiveFails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := q.refreshHeartbeat(ctx, heartbeatKey); err != nil {
				consecutiveFails++
				if consecutiveFails >= maxConsecutiveHeartbeatFailures {
					q.logger.Error("heartbeat loop giving up; cancelling local processing to avoid double-dispatch",
						redact.String("consumer_id", q.consumerID),
						"failures", consecutiveFails,
						redact.Error(err),
					)
					if cancelProcess != nil {
						cancelProcess()
					}
					return
				}
				continue
			}
			consecutiveFails = 0
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
					redact.String("queue", queue),
					redact.Error(err),
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
						redact.String("queue", queue),
						redact.String("dead_consumer_id", deadConsumerID),
						redact.Error(err),
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
					redact.String("queue", queue),
					redact.String("dead_consumer_id", deadConsumerID),
					redact.Error(err),
				)
			}
			return
		}
		if len(items) == 0 {
			// List is empty — delete it so it doesn't reappear in the next scan.
			if err := q.client.Del(ctx, deadProcessingQ).Err(); err != nil && ctx.Err() == nil {
				q.logger.Warn("dead-list cleanup Del failed",
					redact.String("queue", queue),
					redact.String("dead_consumer_id", deadConsumerID),
					redact.Error(err),
				)
			}
			return
		}

		q.logger.Info("reclaiming entries from dead consumer's processing list",
			redact.String("queue", queue),
			redact.String("dead_consumer_id", deadConsumerID),
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
						redact.String("queue", queue),
						redact.String("dead_consumer_id", deadConsumerID),
						redact.Error(err),
					)
				}
				return
			}
			if err := q.client.LRem(ctx, deadProcessingQ, 1, data).Err(); err != nil && ctx.Err() == nil {
				q.logger.Warn("LRem from dead list failed during reclaim",
					redact.String("queue", queue),
					redact.String("dead_consumer_id", deadConsumerID),
					redact.Error(err),
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
		redact.String("queue", queue),
		redact.String("consumer_id", q.consumerID),
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
