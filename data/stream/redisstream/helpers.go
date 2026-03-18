package redisstream

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// deadLetter moves a failed message to the dead-letter stream, then ACKs it.
// Uses a pipeline for single round-trip, minimizing the crash window between
// XADD and XACK that could cause DLQ duplicates.
func (c *Consumer) deadLetter(ctx context.Context, stream, dlStream string, raw goredis.XMessage, reason string) {
	values := make(map[string]any, len(raw.Values)+3)
	for k, v := range raw.Values {
		values[k] = v
	}
	values["dl_reason"] = reason
	values["dl_source_stream"] = stream
	values["dl_source_id"] = raw.ID
	values["dl_timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)

	xaddArgs := &goredis.XAddArgs{
		Stream: dlStream,
		Values: values,
	}
	if c.deadLetterMaxLen > 0 {
		xaddArgs.MaxLen = c.deadLetterMaxLen
		xaddArgs.Approx = true
	}

	// Pipeline XADD+XACK for single round-trip, reducing the crash window
	// that would leave the message in both DLQ and source PEL.
	pipe := c.client.Pipeline()
	pipe.XAdd(ctx, xaddArgs)
	pipe.XAck(ctx, stream, c.group, raw.ID)
	cmds, err := pipe.Exec(ctx)
	if err != nil {
		// Check which command failed for accurate logging.
		if len(cmds) == 0 {
			c.logger.Error("failed to execute dead-letter pipeline",
				"stream", stream,
				"dl_stream", dlStream,
				"redis_id", raw.ID,
				"error", err,
			)
			return
		}
		if cmds[0].Err() != nil {
			c.logger.Error("failed to dead-letter message",
				"stream", stream,
				"dl_stream", dlStream,
				"redis_id", raw.ID,
				"error", cmds[0].Err(),
			)
			return // Don't count as dead-lettered if XADD failed.
		}
		if len(cmds) > 1 && cmds[1].Err() != nil {
			c.logger.Error("failed to ACK dead-lettered message",
				"stream", stream,
				"redis_id", raw.ID,
				"error", cmds[1].Err(),
			)
		}
	}

	c.metrics.messagesDeadLettered.WithLabelValues(stream, c.group).Inc()
}

// claimLoop periodically scans for pending messages that have been idle too
// long (consumer crashed or is too slow) and claims them for processing.
func (c *Consumer) claimLoop(ctx context.Context, stream, dlStream string, handler Handler) {
	ticker := time.NewTicker(c.claimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.claimStaleMessages(ctx, stream, dlStream, handler)
		}
	}
}

// claimStaleMessages uses XAUTOCLAIM to take ownership of messages idle
// longer than claimMinIdle. This recovers from crashed consumers.
func (c *Consumer) claimStaleMessages(ctx context.Context, stream, dlStream string, handler Handler) {
	// Update pending messages gauge for observability.
	if pending, err := c.client.XPending(ctx, stream, c.group).Result(); err == nil {
		c.metrics.pendingMessages.WithLabelValues(stream, c.group).Set(float64(pending.Count))
	}

	// NOTE: Redis 7+ XAUTOCLAIM returns a third element with IDs of PEL entries
	// whose stream entries were deleted (e.g., by MAXLEN trimming). go-redis v9
	// does not expose these deleted IDs, so they remain in the PEL until
	// manually ACKed. This can cause the pendingMessages gauge to be inflated.
	// When go-redis adds support, ACK the deleted IDs here to keep PEL clean.
	startID := "0-0"
	for {
		msgs, newStart, err := c.client.XAutoClaim(ctx, &goredis.XAutoClaimArgs{
			Stream:   stream,
			Group:    c.group,
			Consumer: c.consumer,
			MinIdle:  c.claimMinIdle,
			Start:    startID,
			Count:    c.batchSize,
		}).Result()

		if err != nil {
			if ctx.Err() == nil {
				c.logger.Error("xautoclaim failed",
					"stream", stream,
					"error", err,
				)
			}
			return
		}

		// Batch-fetch delivery counts for all claimed messages in one call
		// instead of N+1 individual XPENDING queries per message.
		retryCounts := c.batchDeliveryCounts(ctx, stream, msgs)

		for _, raw := range msgs {
			if ctx.Err() != nil {
				return
			}
			c.handleMessage(ctx, stream, dlStream, raw, handler, retryCounts[raw.ID])
		}

		// "0-0" means no more messages to claim.
		if newStart == "0-0" || len(msgs) == 0 {
			return
		}
		startID = newStart
	}
}

// batchDeliveryCounts fetches delivery counts for a batch of messages using
// a single XPENDING range query instead of N individual queries.
// Note: no Consumer filter is applied intentionally — XAUTOCLAIM may have
// just transferred ownership, and we need the total delivery count across
// all consumers (not just this one) for accurate retry tracking.
func (c *Consumer) batchDeliveryCounts(ctx context.Context, stream string, msgs []goredis.XMessage) map[string]int64 {
	counts := make(map[string]int64, len(msgs))
	if len(msgs) == 0 {
		return counts
	}

	// Sort IDs to ensure correct XPENDING range bounds — XAUTOCLAIM may
	// return messages out of order during backfill. If startID > endID,
	// Redis returns an empty result and all messages fall back to RetryCount=1.
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	slices.SortFunc(ids, cmp.Compare)
	startID := ids[0]
	endID := ids[len(ids)-1]

	// Use Count larger than len(msgs) because the ID range may contain pending
	// messages from other consumers. Without extra headroom, those interleaved
	// entries could push our messages out of the Count limit, causing missing
	// delivery counts (defaulting to individual fallback queries). 5x provides
	// headroom for moderate consumer groups; missing entries fall back to
	// per-message XPENDING queries below.
	pending, err := c.client.XPendingExt(ctx, &goredis.XPendingExtArgs{
		Stream: stream,
		Group:  c.group,
		Start:  startID,
		End:    endID,
		Count:  int64(len(msgs)) * 5,
	}).Result()

	if err != nil {
		c.logger.Warn("batch delivery count fetch failed, assuming 1 for all",
			"stream", stream,
			"error", err,
		)
		for _, m := range msgs {
			counts[m.ID] = 1
		}
		return counts
	}

	for _, p := range pending {
		counts[p.ID] = p.RetryCount
	}

	// Fall back to individual queries for missing entries. The batch range
	// query may miss messages when many interleaved PEL entries from other
	// consumers exceed the 5x headroom.
	for _, m := range msgs {
		if _, ok := counts[m.ID]; !ok {
			counts[m.ID] = c.getDeliveryCount(ctx, stream, m.ID)
		}
	}

	return counts
}

// getDeliveryCount returns the number of times a message has been delivered.
func (c *Consumer) getDeliveryCount(ctx context.Context, stream, messageID string) int64 {
	// XPENDING stream group start end count consumer — get info for specific message.
	pending, err := c.client.XPendingExt(ctx, &goredis.XPendingExtArgs{
		Stream: stream,
		Group:  c.group,
		Start:  messageID,
		End:    messageID,
		Count:  1,
	}).Result()

	if err != nil {
		c.logger.Warn("failed to get delivery count, assuming 1",
			"stream", stream,
			"redis_id", messageID,
			"error", err,
		)
		return 1
	}
	if len(pending) == 0 {
		return 1 // no pending entry found — assume first delivery
	}

	return pending[0].RetryCount
}

// parseMessage converts a raw Redis stream entry to a Message.
func parseMessage(raw goredis.XMessage) Message {
	msg := Message{
		RedisStreamID: raw.ID,
	}

	if v, ok := raw.Values["id"].(string); ok {
		msg.ID = v
	}
	if v, ok := raw.Values["type"].(string); ok {
		msg.Type = v
	}
	if v, ok := raw.Values["payload"].(string); ok {
		msg.Payload = json.RawMessage(v)
	}
	if v, ok := raw.Values["ts"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			msg.Timestamp = t
		}
	}
	if v, ok := raw.Values["headers"].(string); ok {
		headers := make(map[string]string)
		if err := json.Unmarshal([]byte(v), &headers); err == nil {
			msg.Headers = headers
		}
	}

	return msg
}

// isGroupExistsError detects the "BUSYGROUP Consumer Group name already exists"
// error from Redis. The go-redis library does not expose a typed error for this,
// so string matching is the only option. This is validated in integration tests
// to catch regressions across library upgrades.
func isGroupExistsError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}
