package redisstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/apperror"
	"github.com/bds421/rho-kit/observability/promutil"
	"github.com/bds421/rho-kit/infra/redis"
)

const (
	// defaultBlockDuration is how long XREADGROUP blocks waiting for new messages.
	defaultBlockDuration = 5 * time.Second

	// defaultClaimMinIdle is the minimum idle time before a pending message
	// can be claimed by another consumer (stale lock recovery).
	defaultClaimMinIdle = 5 * time.Minute

	// defaultClaimInterval is how often the claim loop runs.
	defaultClaimInterval = 30 * time.Second

	// defaultBatchSize is the number of messages read per XREADGROUP call.
	defaultBatchSize = 10

	// consumerCleanupTimeout is the maximum time for XGROUP DELCONSUMER
	// when a consumer shuts down. Short because it's a single Redis command
	// and failure is harmless (stale consumers only waste a little memory).
	consumerCleanupTimeout = 5 * time.Second

	// defaultMaxRetries is how many times a message can be redelivered
	// before being sent to the dead-letter stream.
	defaultMaxRetries = 5

	// defaultDeadLetterMaxLen is the approximate maximum number of entries
	// in a dead-letter stream. 0 would mean no limit.
	defaultDeadLetterMaxLen int64 = 10000
)

// Handler processes a single stream message. Return nil to acknowledge,
// or an error to trigger retry/dead-letter logic.
//
// Returning an apperror.PermanentError causes the message to be immediately
// dead-lettered without further retries.
type Handler func(ctx context.Context, msg Message) error

// ConsumerMetrics holds Prometheus collectors for stream consumer monitoring.
type ConsumerMetrics struct {
	messagesConsumed    *prometheus.CounterVec
	messagesFailed      *prometheus.CounterVec
	messagesDeadLettered *prometheus.CounterVec
	processingDuration  *prometheus.HistogramVec
	pendingMessages     *prometheus.GaugeVec
}

// NewConsumerMetrics creates and registers consumer metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewConsumerMetrics(reg prometheus.Registerer) *ConsumerMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &ConsumerMetrics{
		messagesConsumed: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "stream",
				Name:      "messages_consumed_total",
				Help:      "Total messages consumed from streams.",
			},
			[]string{"stream", "group"},
		),
		messagesFailed: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "stream",
				Name:      "messages_failed_total",
				Help:      "Total messages that failed processing.",
			},
			[]string{"stream", "group"},
		),
		messagesDeadLettered: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "stream",
				Name:      "messages_dead_lettered_total",
				Help:      "Total messages moved to dead-letter stream.",
			},
			[]string{"stream", "group"},
		),
		processingDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "redis",
				Subsystem: "stream",
				Name:      "processing_duration_seconds",
				Help:      "Duration of stream message processing.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"stream", "group"},
		),
		pendingMessages: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Subsystem: "stream",
				Name:      "pending_messages",
				Help:      "Number of pending messages in consumer group.",
			},
			[]string{"stream", "group"},
		),
	}

	promutil.RegisterCollector(reg, m.messagesConsumed)
	promutil.RegisterCollector(reg, m.messagesFailed)
	promutil.RegisterCollector(reg, m.messagesDeadLettered)
	promutil.RegisterCollector(reg, m.processingDuration)
	promutil.RegisterCollector(reg, m.pendingMessages)

	return m
}

var defaultConsumerMetrics = NewConsumerMetrics(nil)

// Consumer reads from a Redis stream using consumer groups.
// It handles automatic consumer group creation, pending message claim,
// dead-letter routing, and graceful shutdown.
type Consumer struct {
	client goredis.UniversalClient
	logger *slog.Logger

	group    string
	consumer string

	blockDuration time.Duration
	claimMinIdle  time.Duration
	claimInterval time.Duration
	batchSize     int64
	maxRetries    int64

	// deadLetterStream is the stream where failed messages are sent.
	// Defaults to "{stream}.dead".
	deadLetterStream string

	// deadLetterMaxLen is the approximate max length of the dead-letter stream.
	// Uses MAXLEN ~ (approximate trimming). 0 means no limit. Default is 10000.
	deadLetterMaxLen int64

	metrics *ConsumerMetrics
}

// ConsumerOption configures a Consumer.
type ConsumerOption func(*Consumer)

// WithConsumerLogger sets the logger for the consumer.
func WithConsumerLogger(l *slog.Logger) ConsumerOption {
	return func(c *Consumer) { c.logger = l }
}

// WithConsumerName sets the consumer name within the group. Defaults to
// a UUID v7, which is appropriate for ephemeral consumers in a deployment.
// Panics if name is invalid (empty, contains null bytes, etc.).
func WithConsumerName(name string) ConsumerOption {
	return func(c *Consumer) {
		if err := redis.ValidateName(name, "consumer name"); err != nil {
			panic("redis: " + err.Error())
		}
		c.consumer = name
	}
}

// WithBlockDuration sets how long XREADGROUP blocks waiting for messages.
// Values <= 0 are ignored; the default is used instead.
func WithBlockDuration(d time.Duration) ConsumerOption {
	return func(c *Consumer) {
		if d > 0 {
			c.blockDuration = d
		}
	}
}

// WithClaimMinIdle sets the minimum idle time before claiming pending messages.
// Values <= 0 are ignored; the default is used instead.
func WithClaimMinIdle(d time.Duration) ConsumerOption {
	return func(c *Consumer) {
		if d > 0 {
			c.claimMinIdle = d
		}
	}
}

// WithClaimInterval sets how often the claim loop checks for stale messages.
// Values <= 0 are ignored; the default is used instead.
func WithClaimInterval(d time.Duration) ConsumerOption {
	return func(c *Consumer) {
		if d > 0 {
			c.claimInterval = d
		}
	}
}

// WithBatchSize sets the number of messages fetched per read call.
// Values <= 0 are ignored; the default batch size is used instead.
func WithBatchSize(n int64) ConsumerOption {
	return func(c *Consumer) {
		if n > 0 {
			c.batchSize = n
		}
	}
}

// WithMaxRetries sets how many times a message can be retried before
// dead-lettering. Set to 0 to disable dead-lettering (nack forever).
// Negative values are ignored.
func WithMaxRetries(n int64) ConsumerOption {
	return func(c *Consumer) {
		if n >= 0 {
			c.maxRetries = n
		}
	}
}

// WithDeadLetterStream overrides the default dead-letter stream name.
// Panics if stream name is invalid.
func WithDeadLetterStream(stream string) ConsumerOption {
	return func(c *Consumer) {
		if err := redis.ValidateName(stream, "dead-letter stream"); err != nil {
			panic("redis: " + err.Error())
		}
		c.deadLetterStream = stream
	}
}

// WithDeadLetterMaxLen sets the approximate maximum length of the
// dead-letter stream. Uses MAXLEN ~ (approximate trimming) for O(1)
// performance. 0 means no limit. Negative values are ignored. Default is 10000.
func WithDeadLetterMaxLen(n int64) ConsumerOption {
	return func(c *Consumer) {
		if n >= 0 {
			c.deadLetterMaxLen = n
		}
	}
}

// WithConsumerRegisterer sets the Prometheus registerer for consumer metrics.
// If not set, prometheus.DefaultRegisterer is used.
func WithConsumerRegisterer(reg prometheus.Registerer) ConsumerOption {
	return func(c *Consumer) {
		c.metrics = NewConsumerMetrics(reg)
	}
}

// NewConsumer creates a consumer for the given consumer group.
// Returns an error if group is empty or the consumer ID cannot be generated.
func NewConsumer(client goredis.UniversalClient, group string, opts ...ConsumerOption) (*Consumer, error) {
	if err := redis.ValidateName(group, "consumer group"); err != nil {
		return nil, err
	}
	consumerID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate consumer ID: %w", err)
	}
	c := &Consumer{
		client:           client,
		logger:           slog.Default(),
		group:            group,
		consumer:         consumerID.String(),
		blockDuration:    defaultBlockDuration,
		claimMinIdle:     defaultClaimMinIdle,
		claimInterval:    defaultClaimInterval,
		batchSize:        defaultBatchSize,
		maxRetries:       defaultMaxRetries,
		deadLetterMaxLen: defaultDeadLetterMaxLen,
		metrics:          defaultConsumerMetrics,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Consume starts reading from the stream and dispatching to handler.
// It automatically restarts with exponential backoff on errors.
// Blocks until ctx is cancelled.
//
// Panics if stream name is empty (programming error — fail fast).
func (c *Consumer) Consume(ctx context.Context, stream string, handler Handler) {
	if err := redis.ValidateName(stream, "stream"); err != nil {
		panic("redis: " + err.Error())
	}
	redis.RunWithBackoff(ctx, c.logger, "stream consumer", func(ctx context.Context) error {
		return c.consumeOnce(ctx, stream, handler)
	})
}

// consumeOnce runs a single consumer session. Returns on error or context cancellation.
func (c *Consumer) consumeOnce(ctx context.Context, stream string, handler Handler) error {
	dlStream := c.deadLetterStream
	if dlStream == "" {
		dlStream = stream + ".dead"
	}

	// Ensure the consumer group exists. MKSTREAM creates the stream if needed.
	// Using "0" as start ID means the group will process all existing messages
	// on first creation. This is intentional — in this framework, streams are
	// created before consumers start, so "0" ensures no messages are missed.
	// Note: if the group is deleted and re-created, all historical messages
	// will be reprocessed. Use XGROUP SETID to adjust the start position if
	// this is not desired.
	err := c.client.XGroupCreateMkStream(ctx, stream, c.group, "0").Err()
	if err != nil && !isGroupExistsError(err) {
		return fmt.Errorf("create consumer group: %w", err)
	}

	// Start the claim loop for stale pending messages.
	claimCtx, claimCancel := context.WithCancel(ctx)
	claimDone := make(chan struct{})
	go func() {
		defer close(claimDone)
		c.claimLoop(claimCtx, stream, dlStream, handler)
	}()
	defer func() {
		claimCancel()
		<-claimDone // Wait for the claim loop to finish before restarting.
		c.removeConsumer(stream)
		// Zero the pending gauge so it doesn't report stale values after shutdown.
		c.metrics.pendingMessages.WithLabelValues(stream, c.group).Set(0)
	}()

	// First, process any messages that were previously delivered to this
	// consumer but not yet acknowledged (pending entries after restart).
	if err := c.processPending(ctx, stream, dlStream, handler); err != nil {
		return err
	}

	// Then read new messages.
	return c.readNew(ctx, stream, dlStream, handler)
}

// removeConsumer removes this consumer from the consumer group to prevent
// accumulation of stale consumer entries in Redis. Only logs on failure —
// stale consumers are harmless (they just waste a small amount of memory).
func (c *Consumer) removeConsumer(stream string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), consumerCleanupTimeout)
	defer cancel()
	if err := c.client.XGroupDelConsumer(cleanupCtx, stream, c.group, c.consumer).Err(); err != nil {
		c.logger.Warn("failed to remove consumer from group",
			"stream", stream,
			"group", c.group,
			"consumer", c.consumer,
			"error", err,
		)
	}
}

// maxPendingPerRestart limits how many pending messages are processed before
// switching to readNew. This prevents large backlogs from starving real-time
// message processing and avoids OOM when millions of messages are pending.
const maxPendingPerRestart = 10_000

// processPending reads messages from this consumer's pending entries list (PEL).
// These are messages previously delivered but not ACKed (e.g. after a crash).
// Processes at most [maxPendingPerRestart] messages before returning so that
// readNew can interleave with pending recovery for large backlogs.
func (c *Consumer) processPending(ctx context.Context, stream, dlStream string, handler Handler) error {
	lastID := "0-0"
	totalProcessed := int64(0)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if totalProcessed >= maxPendingPerRestart {
			c.logger.Info("pending message limit reached, switching to new messages",
				"stream", stream, "processed", totalProcessed)
			return nil
		}

		msgs, err := c.client.XReadGroup(ctx, &goredis.XReadGroupArgs{
			Group:    c.group,
			Consumer: c.consumer,
			Streams:  []string{stream, lastID},
			Count:    c.batchSize,
			Block:    -1, // negative = no BLOCK arg sent; returns immediately if no pending (0 would send BLOCK 0 = infinite block)
		}).Result()

		if err != nil {
			if errors.Is(err, goredis.Nil) {
				return nil // no more pending
			}
			return fmt.Errorf("xreadgroup pending: %w", err)
		}

		if len(msgs) == 0 || len(msgs[0].Messages) == 0 {
			return nil // no more pending
		}

		batch := msgs[0].Messages
		// Batch-fetch delivery counts for pending messages to avoid N+1.
		retryCounts := c.batchDeliveryCounts(ctx, stream, batch)
		for _, raw := range batch {
			if ctx.Err() != nil {
				return nil
			}
			c.handleMessage(ctx, stream, dlStream, raw, handler, retryCounts[raw.ID])
			lastID = raw.ID
			totalProcessed++
		}
	}
}

// readNew reads new (undelivered) messages using the special ">" ID.
func (c *Consumer) readNew(ctx context.Context, stream, dlStream string, handler Handler) error {
	for {
		msgs, err := c.client.XReadGroup(ctx, &goredis.XReadGroupArgs{
			Group:    c.group,
			Consumer: c.consumer,
			Streams:  []string{stream, ">"},
			Count:    c.batchSize,
			Block:    c.blockDuration,
		}).Result()

		if err != nil {
			if errors.Is(err, goredis.Nil) {
				continue // timeout, no new messages
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("xreadgroup: %w", err)
		}

		for _, s := range msgs {
			for _, raw := range s.Messages {
				if ctx.Err() != nil {
					return nil
				}
				c.handleMessage(ctx, stream, dlStream, raw, handler, -1)
			}
		}
	}
}

const (
	// ackTimeout is the maximum time allowed for post-handler operations
	// (ACK, dead-letter write) which must succeed even if the handler cancelled
	// the parent context.
	ackTimeout = 10 * time.Second

	// handlerShutdownTimeout is the grace period given to a handler that is
	// still running when the parent context is cancelled. This prevents message
	// loss by allowing in-flight handlers to complete their work.
	handlerShutdownTimeout = 30 * time.Second
)

// handleMessage parses, dispatches, and handles the result of a single message.
// If knownRetryCount >= 0, it is used directly; otherwise the delivery count is
// fetched via XPENDING.
func (c *Consumer) handleMessage(ctx context.Context, stream, dlStream string, raw goredis.XMessage, handler Handler, knownRetryCount int64) {
	msg := parseMessage(raw)

	// Give the handler a shutdown-aware context: derive from the parent
	// context with a grace period so in-flight handlers can complete when
	// shutdown is signaled, rather than being killed immediately.
	var handlerCtx context.Context
	var handlerCancel context.CancelFunc
	if ctx.Err() != nil {
		// Parent already cancelled — use a fresh background context.
		handlerCtx, handlerCancel = context.WithTimeout(context.Background(), handlerShutdownTimeout)
	} else {
		handlerCtx, handlerCancel = context.WithTimeout(ctx, handlerShutdownTimeout)
	}
	defer handlerCancel()

	start := time.Now()
	err := handler(handlerCtx, msg)
	duration := time.Since(start)

	c.metrics.processingDuration.WithLabelValues(stream, c.group).Observe(duration.Seconds())

	// Use a fresh context for post-handler operations (ACK, dead-letter).
	// The handler may have cancelled the parent context, but these operations
	// must still succeed to avoid message duplication or loss.
	//
	// Note: there is a small crash window between XADD (dead-letter write)
	// and XACK. If the process crashes in that window, the message will exist
	// in both the source stream (pending) and the dead-letter stream. This is
	// why handlers MUST be idempotent (see Consume godoc and doc.go).
	ackCtx, ackCancel := context.WithTimeout(context.Background(), ackTimeout)
	defer ackCancel()

	if err == nil {
		if ackErr := c.client.XAck(ackCtx, stream, c.group, raw.ID).Err(); ackErr != nil {
			c.logger.Error("failed to ACK message",
				"stream", stream,
				"redis_id", raw.ID,
				"error", ackErr,
			)
		}
		c.metrics.messagesConsumed.WithLabelValues(stream, c.group).Inc()
		return
	}

	c.metrics.messagesFailed.WithLabelValues(stream, c.group).Inc()

	// Check if the error is permanent (no retry will help).
	if apperror.IsPermanent(err) {
		c.logger.Error("permanent error, dead-lettering message",
			"stream", stream,
			"redis_id", raw.ID,
			"msg_id", msg.ID,
			"error", err,
		)
		c.deadLetter(ackCtx, stream, dlStream, raw, "permanent_error")
		return
	}

	// Use pre-fetched retry count if available, otherwise fetch via XPENDING.
	deliveryCount := knownRetryCount
	if deliveryCount < 0 {
		deliveryCount = c.getDeliveryCount(ackCtx, stream, raw.ID)
	}
	c.handleRetryOrDeadLetter(ackCtx, stream, dlStream, raw, msg, deliveryCount, err)
}

// handleRetryOrDeadLetter checks the delivery count and either dead-letters
// the message or leaves it pending for retry.
func (c *Consumer) handleRetryOrDeadLetter(ackCtx context.Context, stream, dlStream string, raw goredis.XMessage, msg Message, deliveryCount int64, err error) {
	if c.maxRetries > 0 && deliveryCount > c.maxRetries {
		c.logger.Error("max retries exceeded, dead-lettering message",
			"stream", stream,
			"redis_id", raw.ID,
			"msg_id", msg.ID,
			"delivery_count", deliveryCount,
			"max_retries", c.maxRetries,
			"error", err,
		)
		c.deadLetter(ackCtx, stream, dlStream, raw, "max_retries_exceeded")
		return
	}

	// Leave the message in pending — it will be redelivered on next
	// processPending() call or claimed by another consumer.
	c.logger.Warn("message processing failed, will retry",
		"stream", stream,
		"redis_id", raw.ID,
		"msg_id", msg.ID,
		"delivery_count", deliveryCount,
		"max_retries", c.maxRetries,
		"error", err,
	)
}
