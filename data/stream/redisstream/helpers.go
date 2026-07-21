package redisstream

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// maxStreamHeadersBytes caps the raw bytes the headers JSON field is
// allowed to occupy on the wire before parseMessage will reject a
// delivery. Aligned with the producer-side MaxTotalHeaderBytes cap
// (256 KiB of name+value) plus JSON structural overhead for up to
// MaxHeaderCount entries, so a producer-accepted header set is not
// silently dead-lettered at consume time. Still bounds hostile peers
// that write multi-MB headers fields before json.Unmarshal.
const maxStreamHeadersBytes = MaxTotalHeaderBytes + 64*1024 // ~320 KiB

// deadLetter moves a failed message to the dead-letter stream, then ACKs it.
// Uses a pipeline for single round-trip, minimizing the crash window between
// XADD and XACK that could cause DLQ duplicates.
func (c *Consumer) deadLetter(ctx context.Context, stream, dlStream string, raw goredis.XMessage, reason string) {
	// Cap payload/header fields so oversize/hostile entries rejected by
	// ValidateMessage cannot amplify into the DLQ (entry-count MAXLEN is
	// not a byte bound). Keep metadata + truncated forensic sample.
	values := make(map[string]any, len(raw.Values)+4)
	maxField := c.maxPayloadSize
	if maxField <= 0 {
		maxField = defaultStreamMaxPayloadSize
	}
	for k, v := range raw.Values {
		switch vv := v.(type) {
		case string:
			if len(vv) > maxField {
				values[k] = vv[:maxField]
				values["dl_truncated_"+k] = "true"
			} else {
				values[k] = vv
			}
		case []byte:
			if len(vv) > maxField {
				values[k] = string(vv[:maxField])
				values["dl_truncated_"+k] = "true"
			} else {
				values[k] = string(vv)
			}
		default:
			values[k] = v
		}
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

	// XADD then XACK sequentially — not pipelined. Redis pipelines are
	// not transactions: if XADD fails (OOM/WRONGTYPE) while a pipelined
	// XACK succeeds, the message is removed from the PEL with no DLQ
	// copy and is silently lost. Accept the extra RTT to keep
	// at-least-once semantics under degraded Redis conditions.
	if err := c.client.XAdd(ctx, xaddArgs).Err(); err != nil {
		c.logger.Error("failed to dead-letter message",
			redact.String("stream", stream),
			redact.String("dl_stream", dlStream),
			redact.String("redis_id", raw.ID),
			redact.Error(err),
		)
		return // Message stays in source PEL for a later dead-letter attempt.
	}
	if err := c.client.XAck(ctx, stream, c.group, raw.ID).Err(); err != nil {
		c.logger.Error("failed to ACK dead-lettered message",
			redact.String("stream", stream),
			redact.String("redis_id", raw.ID),
			redact.Error(err),
		)
		// XADD succeeded but XACK failed: the message stays in the source
		// PEL and will be dead-lettered again on a later delivery, writing
		// a second DLQ entry. Skip the increment here so the counter tracks
		// unique dead-lettered messages rather than double-counting this one.
		return
	}

	c.metrics.messagesDeadLettered.WithLabelValues(c.metricLabel(stream), c.metricGroupLabel()).Inc()
}

// claimLoop periodically scans for pending messages that have been idle too
// long (consumer crashed or is too slow) and claims them for processing.
func (c *Consumer) claimLoop(ctx context.Context, stream, dlStream string, handler Handler) {
	ticker := time.NewTicker(c.claimInterval)
	defer ticker.Stop()
	// Also sweep empty, long-idle consumer names left by prior process
	// exits that skipped XGROUP DELCONSUMER (pending PEL at shutdown).
	cleanupEvery := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.claimStaleMessages(ctx, stream, dlStream, handler)
			cleanupEvery++
			if cleanupEvery%5 == 0 {
				c.purgeStaleConsumers(ctx, stream)
			}
		}
	}
}

// staleConsumerIdle is how long a group consumer must sit with an empty
// PEL before we XGROUP DELCONSUMER it. Generous so an idle-but-live
// replica is never deleted mid-deploy.
const staleConsumerIdle = 10 * time.Minute

// purgeStaleConsumers deletes group consumer entries that have no pending
// messages and have been idle longer than [staleConsumerIdle]. Complements
// removeConsumer: crash/SIGKILL paths never clean themselves, and graceful
// shutdown skips DELCONSUMER while PEL is non-empty — after XAUTOCLAIM
// drains those entries the empty name would otherwise remain forever.
func (c *Consumer) purgeStaleConsumers(ctx context.Context, stream string) {
	if ctx.Err() != nil {
		return
	}
	infos, err := c.client.XInfoConsumers(ctx, stream, c.group).Result()
	if err != nil {
		if ctx.Err() == nil {
			c.logger.Debug("xinfo consumers for stale cleanup failed",
				redact.String("stream", stream),
				redact.Error(err),
			)
		}
		return
	}
	for _, info := range infos {
		if ctx.Err() != nil {
			return
		}
		if info.Name == c.consumer {
			continue // never delete ourselves
		}
		if info.Pending > 0 {
			continue
		}
		// go-redis already converts Redis idle-ms into time.Duration.
		if info.Idle < staleConsumerIdle {
			continue
		}
		if err := c.client.XGroupDelConsumer(ctx, stream, c.group, info.Name).Err(); err != nil && ctx.Err() == nil {
			c.logger.Debug("failed to purge stale consumer",
				redact.String("stream", stream),
				redact.String("group", c.group),
				redact.String("consumer", info.Name),
				redact.Error(err),
			)
		}
	}
}

// claimStaleMessages uses XAUTOCLAIM to take ownership of messages idle
// longer than claimMinIdle. This recovers from crashed consumers.
func (c *Consumer) claimStaleMessages(ctx context.Context, stream, dlStream string, handler Handler) {
	// Update pending messages gauge for observability.
	streamLabel := c.metricLabel(stream)
	groupLabel := c.metricGroupLabel()
	if pending, err := c.client.XPending(ctx, stream, c.group).Result(); err == nil {
		c.metrics.pendingMessages.WithLabelValues(streamLabel, groupLabel).Set(float64(pending.Count))
	} else if ctx.Err() == nil {
		// Surface a stale-gauge signal so operators know the metric may be frozen.
		c.logger.Debug("xpending gauge refresh failed",
			redact.String("stream", stream),
			redact.Error(err),
		)
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
					redact.String("stream", stream),
					redact.Error(err),
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

		// "0-0" means the cursor has wrapped: no more PEL to scan. A non-"0-0"
		// cursor with an empty page is legal — XAUTOCLAIM may scan a window
		// holding no claimable entries (e.g. all younger than claimMinIdle)
		// while older idle entries remain deeper in the PEL. Stopping on the
		// empty page would abandon the scan and restart from "0-0" next tick,
		// repeatedly re-scanning only the head window and starving those
		// entries of recovery. Advance the cursor instead.
		if newStart == "0-0" {
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

	// Sort IDs with a Redis stream-ID aware comparator so XPENDING range
	// bounds are numeric (ms, seq), not lexicographic. String sort mis-orders
	// same-millisecond seqs of differing digit widths (e.g. ...-2 vs ...-10),
	// producing start>end and forcing the N+1 per-message fallback.
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	sortStreamIDs(ids)
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
		// Fail closed toward retry, not toward inflating the retry budget:
		// assuming deliveryCount=1 on XPENDING failure prevents poison
		// messages from ever reaching maxRetries. Leave 0 (unknown) so
		// handleRetryOrDeadLetter will not dead-letter based on a guess.
		c.logger.Warn("batch delivery count fetch failed; treating counts as unknown (will retry, not DLQ)",
			redact.String("stream", stream),
			redact.Error(err),
		)
		for _, m := range msgs {
			counts[m.ID] = 0
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
		c.logger.Warn("failed to get delivery count; treating as unknown (will retry, not DLQ)",
			redact.String("stream", stream),
			redact.String("redis_id", messageID),
			redact.Error(err),
		)
		return 0
	}
	if len(pending) == 0 {
		// No pending entry: treat as first delivery (count 1), not unknown.
		return 1
	}

	return pending[0].RetryCount
}

// parseMessage converts a raw Redis stream entry to a Message.
func parseMessage(raw goredis.XMessage) (Message, error) {
	msg := Message{
		RedisStreamID: raw.ID,
	}

	if v, ok := raw.Values["id"].(string); ok {
		msg.ID = v
	} else if _, exists := raw.Values["id"]; exists {
		return msg, fmt.Errorf("%w: id field must be a string", ErrInvalidMessage)
	}
	if v, ok := raw.Values["type"].(string); ok {
		msg.Type = v
	} else if _, exists := raw.Values["type"]; exists {
		return msg, fmt.Errorf("%w: type field must be a string", ErrInvalidMessage)
	}
	if v, ok := raw.Values["payload"].(string); ok {
		msg.Payload = json.RawMessage(v)
	} else if _, exists := raw.Values["payload"]; exists {
		return msg, fmt.Errorf("%w: payload field must be a string", ErrInvalidMessage)
	}
	if v, ok := raw.Values["ts"].(string); ok {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return msg, redact.WrapSentinel(
				fmt.Errorf("%w: timestamp must be RFC3339Nano", ErrInvalidMessage), err,
			)
		}
		msg.Timestamp = t
	} else if _, exists := raw.Values["ts"]; exists {
		return msg, fmt.Errorf("%w: timestamp field must be a string", ErrInvalidMessage)
	}
	if v, ok := raw.Values["headers"].(string); ok {
		// Cap raw headers JSON bytes BEFORE unmarshal so a hostile or
		// corrupt stream writer cannot OOM the consumer with a 500MB
		// "headers" field. The cap (32 KiB) is comfortably above any
		// realistic header set (MaxMessageHeaderValueBytes × 32 entries
		// + JSON structural bytes); ValidateMessage applies the per-entry
		// limits AFTER unmarshal so the parse cost stays bounded.
		if len(v) > maxStreamHeadersBytes {
			return msg, fmt.Errorf("%w: headers JSON exceeds %d bytes", ErrInvalidMessage, maxStreamHeadersBytes)
		}
		headers := make(map[string]string)
		if err := json.Unmarshal([]byte(v), &headers); err != nil {
			return msg, redact.WrapSentinel(
				fmt.Errorf("%w: headers must be a JSON object", ErrInvalidMessage), err,
			)
		}
		msg.Headers = headers
	} else if _, exists := raw.Values["headers"]; exists {
		return msg, fmt.Errorf("%w: headers field must be a string", ErrInvalidMessage)
	}

	return msg, nil
}

// isGroupExistsError detects the "BUSYGROUP Consumer Group name already exists"
// error from Redis. The go-redis library does not expose a typed error for this,
// so string matching is the only option. This is validated in integration tests
// to catch regressions across library upgrades.
func isGroupExistsError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}

// sortStreamIDs sorts Redis stream IDs in ascending (ms, seq) order in place.
func sortStreamIDs(ids []string) {
	// insertion sort is fine for claim batch sizes (default 10).
	for i := 1; i < len(ids); i++ {
		j := i
		for j > 0 && compareStreamID(ids[j-1], ids[j]) > 0 {
			ids[j-1], ids[j] = ids[j], ids[j-1]
			j--
		}
	}
}

// compareStreamID compares Redis stream IDs ("<ms>-<seq>") numerically.
// Malformed IDs sort after well-formed ones via string fallback.
func compareStreamID(a, b string) int {
	ams, aseq, aok := parseStreamID(a)
	bms, bseq, bok := parseStreamID(b)
	if aok && bok {
		if ams < bms {
			return -1
		}
		if ams > bms {
			return 1
		}
		if aseq < bseq {
			return -1
		}
		if aseq > bseq {
			return 1
		}
		return 0
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func parseStreamID(id string) (ms, seq uint64, ok bool) {
	dash := strings.IndexByte(id, '-')
	if dash <= 0 || dash == len(id)-1 {
		return 0, 0, false
	}
	var err error
	ms, err = strconv.ParseUint(id[:dash], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	seq, err = strconv.ParseUint(id[dash+1:], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return ms, seq, true
}

