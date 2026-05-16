package redisstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/core/v2/redact"
	kitstream "github.com/bds421/rho-kit/data/v2/stream"
	"github.com/bds421/rho-kit/infra/redis/v2"
	"github.com/bds421/rho-kit/observability/v2/promutil"
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
	messagesConsumed     *prometheus.CounterVec
	messagesFailed       *prometheus.CounterVec
	messagesDeadLettered *prometheus.CounterVec
	processingDuration   *prometheus.HistogramVec
	pendingMessages      *prometheus.GaugeVec
}

// ConsumerMetricsOption configures the redisstream consumer metric constructor.
type ConsumerMetricsOption func(*consumerMetricsConfig)

type consumerMetricsConfig struct {
	registerer prometheus.Registerer
}

// WithConsumerMetricsRegisterer pins the Prometheus registerer used
// for consumer metrics. Unset defaults to [prometheus.DefaultRegisterer];
// passing nil panics.
func WithConsumerMetricsRegisterer(reg prometheus.Registerer) ConsumerMetricsOption {
	if reg == nil {
		panic("redisstream: WithConsumerMetricsRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *consumerMetricsConfig) { c.registerer = reg }
}

// NewConsumerMetrics creates and registers consumer metrics. Pass
// [WithConsumerMetricsRegisterer] to use a non-default registry.
func NewConsumerMetrics(opts ...ConsumerMetricsOption) *ConsumerMetrics {
	cfg := consumerMetricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("redisstream: NewConsumerMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

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

	m.messagesConsumed = promutil.MustRegisterOrGet(reg, m.messagesConsumed)
	m.messagesFailed = promutil.MustRegisterOrGet(reg, m.messagesFailed)
	m.messagesDeadLettered = promutil.MustRegisterOrGet(reg, m.messagesDeadLettered)
	m.processingDuration = promutil.MustRegisterOrGet(reg, m.processingDuration)
	m.pendingMessages = promutil.MustRegisterOrGet(reg, m.pendingMessages)

	return m
}

var defaultConsumerMetrics = sync.OnceValue(func() *ConsumerMetrics { return NewConsumerMetrics() })

// Consumer reads from a Redis stream using consumer groups.
// It handles automatic consumer group creation, pending message claim,
// dead-letter routing, and graceful shutdown.
//
// A Consumer is bound to exactly one stream — call [Consumer.Consume]
// once per Consumer instance. Calling Consume for a second stream from
// the same Consumer panics, because all calls share a single consumer
// name and that name is what XGROUP DELCONSUMER targets at shutdown
// (cross-stream interactions get tangled fast). Use [StartConsumers] for
// multi-stream services — it clones one Consumer per binding internally.
type Consumer struct {
	client goredis.UniversalClient
	logger *slog.Logger

	group    string
	consumer string

	blockDuration  time.Duration
	claimMinIdle   time.Duration
	claimInterval  time.Duration
	batchSize      int64
	maxRetries     int64
	maxPayloadSize int

	// deadLetterStream is the stream where failed messages are sent.
	// Defaults to "{stream}.dead".
	deadLetterStream string

	// deadLetterMaxLen is the approximate max length of the dead-letter stream.
	// Uses MAXLEN ~ (approximate trimming). 0 means no limit. Default is 10000.
	deadLetterMaxLen int64

	metrics *ConsumerMetrics

	// consumed records whether Consume has been called. A Consumer is
	// single-use to keep the consumer-name → stream mapping unambiguous.
	consumed atomic.Bool
}

// ConsumerOption configures a Consumer.
type ConsumerOption func(*Consumer)

// Group returns the consumer-group name this Consumer was
// constructed with. Used by the redisbackend wrapper to validate
// Binding.ConsumerGroup equality (audit FR-064).
func (c *Consumer) Group() string {
	if c == nil {
		return ""
	}
	return c.group
}

// WithConsumerLogger sets the logger for the consumer.
//
// FR-063 [MED]: a nil logger is normalised to slog.Default() so a
// caller omitting/passing nil cannot trigger a runtime nil-deref
// later when the consumer logs an error.
func WithConsumerLogger(l *slog.Logger) ConsumerOption {
	if l == nil {
		l = slog.Default()
	}
	return func(c *Consumer) { c.logger = l }
}

// WithConsumerName sets the consumer name within the group. Defaults to
// a UUID v7, which is appropriate for ephemeral consumers in a deployment.
// Panics if name is invalid (empty, contains null bytes, etc.).
func WithConsumerName(name string) ConsumerOption {
	return func(c *Consumer) {
		if err := redis.ValidateName(name, "consumer name"); err != nil {
			panic("redisstream: WithConsumerName invalid consumer name")
		}
		c.consumer = name
	}
}

// WithBlockDuration sets how long XREADGROUP blocks waiting for messages.
// The duration must be positive.
func WithBlockDuration(d time.Duration) ConsumerOption {
	if d <= 0 {
		panic("redisstream: WithBlockDuration requires a positive duration")
	}
	return func(c *Consumer) {
		c.blockDuration = d
	}
}

// WithClaimMinIdle sets the minimum idle time before claiming pending messages.
// The duration must be positive.
func WithClaimMinIdle(d time.Duration) ConsumerOption {
	if d <= 0 {
		panic("redisstream: WithClaimMinIdle requires a positive duration")
	}
	return func(c *Consumer) {
		c.claimMinIdle = d
	}
}

// WithClaimInterval sets how often the claim loop checks for stale messages.
// The duration must be positive.
func WithClaimInterval(d time.Duration) ConsumerOption {
	if d <= 0 {
		panic("redisstream: WithClaimInterval requires a positive duration")
	}
	return func(c *Consumer) {
		c.claimInterval = d
	}
}

// WithBatchSize sets the number of messages fetched per read/claim call.
// The size must be positive and at most [MaxBatchMessages].
func WithBatchSize(n int64) ConsumerOption {
	if n <= 0 {
		panic("redisstream: WithBatchSize requires n > 0")
	}
	if n > MaxBatchMessages {
		panic("redisstream: WithBatchSize exceeds MaxBatchMessages")
	}
	return func(c *Consumer) {
		c.batchSize = n
	}
}

// WithMaxRetries sets how many times a message can be retried before
// dead-lettering. Set to 0 to disable dead-lettering (nack forever).
// Negative values panic.
func WithMaxRetries(n int64) ConsumerOption {
	if n < 0 {
		panic("redisstream: WithMaxRetries requires n >= 0")
	}
	return func(c *Consumer) {
		c.maxRetries = n
	}
}

// WithConsumerMaxPayloadSize sets the maximum stream message payload size
// accepted by the consumer before handler dispatch. The default is 1 MiB.
// Set to 0 to disable the limit entirely. Negative values panic.
func WithConsumerMaxPayloadSize(n int) ConsumerOption {
	if n < 0 {
		panic("redisstream: WithConsumerMaxPayloadSize requires n >= 0")
	}
	return func(c *Consumer) {
		c.maxPayloadSize = n
	}
}

// WithDeadLetterStream overrides the default dead-letter stream name.
// Panics if stream name is invalid.
func WithDeadLetterStream(stream string) ConsumerOption {
	return func(c *Consumer) {
		if err := redis.ValidateName(stream, "dead-letter stream"); err != nil {
			panic("redisstream: WithDeadLetterStream invalid dead-letter stream name")
		}
		c.deadLetterStream = stream
	}
}

// WithDeadLetterMaxLen sets the approximate maximum length of the
// dead-letter stream. Uses MAXLEN ~ (approximate trimming) for O(1)
// performance. 0 means no limit. Negative values panic. Default is 10000.
func WithDeadLetterMaxLen(n int64) ConsumerOption {
	if n < 0 {
		panic("redisstream: WithDeadLetterMaxLen requires n >= 0")
	}
	return func(c *Consumer) {
		c.deadLetterMaxLen = n
	}
}

// WithConsumerRegisterer sets the Prometheus registerer for consumer
// metrics. If not set, prometheus.DefaultRegisterer is used. The
// consumer/producer naming distinction stays in this package because
// the package exports both a Consumer and a Producer side-by-side; a
// generic "WithMetricsRegisterer" would collide.
func WithConsumerRegisterer(reg prometheus.Registerer) ConsumerOption {
	return func(c *Consumer) {
		if reg == nil {
			c.metrics = NewConsumerMetrics()
			return
		}
		c.metrics = NewConsumerMetrics(WithConsumerMetricsRegisterer(reg))
	}
}

// NewConsumer creates a consumer for the given consumer group.
// Returns an error if group is empty or the consumer ID cannot be generated.
// Panics if client is nil — a miswired consumer would otherwise dereference
// nil on the first Consume.
func NewConsumer(client goredis.UniversalClient, group string, opts ...ConsumerOption) (*Consumer, error) {
	if client == nil {
		panic("redisstream: NewConsumer requires a non-nil Redis client")
	}
	if err := redis.ValidateName(group, "consumer group"); err != nil {
		return nil, err
	}
	c := &Consumer{
		client:           client,
		logger:           slog.Default(),
		group:            group,
		consumer:         id.New(),
		blockDuration:    defaultBlockDuration,
		claimMinIdle:     defaultClaimMinIdle,
		claimInterval:    defaultClaimInterval,
		batchSize:        defaultBatchSize,
		maxRetries:       defaultMaxRetries,
		maxPayloadSize:   defaultStreamMaxPayloadSize,
		deadLetterMaxLen: defaultDeadLetterMaxLen,
		metrics:          defaultConsumerMetrics(),
	}
	for _, o := range opts {
		if o == nil {
			panic("redisstream: NewConsumer option must not be nil")
		}
		o(c)
	}
	return c, nil
}

func (c *Consumer) ready() error {
	if c == nil ||
		c.client == nil ||
		c.logger == nil ||
		c.blockDuration <= 0 ||
		c.claimMinIdle <= 0 ||
		c.claimInterval <= 0 ||
		c.batchSize <= 0 ||
		c.maxRetries < 0 ||
		c.maxPayloadSize < 0 ||
		c.deadLetterMaxLen < 0 ||
		c.metrics == nil {
		return kitstream.ErrInvalidStream
	}
	if err := redis.ValidateName(c.group, "consumer group"); err != nil {
		return kitstream.ErrInvalidStream
	}
	if err := redis.ValidateName(c.consumer, "consumer name"); err != nil {
		return kitstream.ErrInvalidStream
	}
	if c.deadLetterStream != "" {
		if err := redis.ValidateName(c.deadLetterStream, "dead-letter stream"); err != nil {
			return kitstream.ErrInvalidStream
		}
	}
	return nil
}

// Consume starts reading from the stream and dispatching to handler.
// It automatically restarts with exponential backoff on errors.
// Blocks until ctx is cancelled.
//
// A single Consumer instance must be used for exactly one stream — see
// the [Consumer] doc. Panics if stream name is empty, handler is nil
// (programming errors), or if Consume has already been called for this
// Consumer (multi-stream usage). Use [StartConsumers] for multi-stream
// services.
func (c *Consumer) Consume(ctx context.Context, stream string, handler Handler) {
	if err := c.ready(); err != nil {
		panic("redisstream: Consume consumer is invalid")
	}
	if err := redis.ValidateName(stream, "stream"); err != nil {
		panic("redisstream: Consume invalid stream name")
	}
	if handler == nil {
		panic("redisstream: Consumer.Consume requires a non-nil handler")
	}
	if !c.consumed.CompareAndSwap(false, true) {
		panic("redisstream: Consumer.Consume called for a second stream — create a separate Consumer per stream (see StartConsumers)")
	}
	redis.RunWithBackoff(ctx, c.logger, "stream consumer", func(ctx context.Context) error {
		return c.consumeOnce(ctx, stream, handler)
	})
}

// cloneForStream returns a copy of c with a freshly-generated consumer ID,
// for use binding to a different stream. Used by [StartConsumers] so each
// goroutine owns a distinct consumer name in its stream's group.
func (c *Consumer) cloneForStream() (*Consumer, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	return &Consumer{
		client:           c.client,
		logger:           c.logger,
		group:            c.group,
		consumer:         id.New(),
		blockDuration:    c.blockDuration,
		claimMinIdle:     c.claimMinIdle,
		claimInterval:    c.claimInterval,
		batchSize:        c.batchSize,
		maxRetries:       c.maxRetries,
		maxPayloadSize:   c.maxPayloadSize,
		deadLetterStream: c.deadLetterStream,
		deadLetterMaxLen: c.deadLetterMaxLen,
		metrics:          c.metrics,
	}, nil
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
		return redact.WrapError("create consumer group", err)
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
		c.removeConsumer(ctx, stream)
		// Zero the pending gauge so it doesn't report stale values after shutdown.
		c.metrics.pendingMessages.WithLabelValues(streamMetricLabel(stream), groupMetricLabel(c.group)).Set(0)
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
//
// CRITICAL: Redis XGROUP DELCONSUMER deletes the named consumer AND its
// pending entries list (PEL) entries. The group's last-delivered-ID has
// already advanced past those entries, so a "> " read will not redeliver
// them. If we delete a consumer with pending entries, those messages are
// silently lost — even though they could otherwise be recovered by
// XAUTOCLAIM or by processPending after restart.
//
// To preserve durability, only delete this consumer when its PEL is empty.
// A consumer with pending entries is left in place so XAUTOCLAIM (running
// in sibling consumers) can recover the messages after claimMinIdle.
func (c *Consumer) removeConsumer(ctx context.Context, stream string) {
	cleanupCtx, cancel := streamDetachedTimeout(ctx, consumerCleanupTimeout)
	defer cancel()

	pending, err := c.client.XPendingExt(cleanupCtx, &goredis.XPendingExtArgs{
		Stream:   stream,
		Group:    c.group,
		Start:    "-",
		End:      "+",
		Count:    1,
		Consumer: c.consumer,
	}).Result()
	if err != nil {
		c.logger.Warn("failed to check pending entries before consumer cleanup, skipping deletion to preserve durability",
			redact.String("stream", stream),
			redact.String("group", c.group),
			redact.String("consumer", c.consumer),
			redact.Error(err),
		)
		return
	}
	if len(pending) > 0 {
		c.logger.Info("consumer has pending entries, leaving in group for XAUTOCLAIM recovery",
			redact.String("stream", stream),
			redact.String("group", c.group),
			redact.String("consumer", c.consumer),
			"pending_count", len(pending),
		)
		return
	}

	if err := c.client.XGroupDelConsumer(cleanupCtx, stream, c.group, c.consumer).Err(); err != nil {
		c.logger.Warn("failed to remove consumer from group",
			redact.String("stream", stream),
			redact.String("group", c.group),
			redact.String("consumer", c.consumer),
			redact.Error(err),
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
				redact.String("stream", stream), "processed", totalProcessed)
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
			// With Block:-1 the server returns an empty result rather than
			// goredis.Nil when no pending messages exist (handled by the
			// len() check below). Any error here is a real protocol/network
			// failure that must propagate.
			return redact.WrapError("xreadgroup pending", err)
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
			return redact.WrapError("xreadgroup", err)
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

func streamDetachedTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		// A nil ctx from a producer/consumer call is always a caller
		// bug; silently substituting Background hides the bug and lets
		// shutdown signals get dropped. Fail loud.
		panic("redisstream: nil ctx")
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// handleMessage parses, dispatches, and handles the result of a single message.
// If knownRetryCount >= 0, it is used directly; otherwise the delivery count is
// fetched via XPENDING.
func (c *Consumer) handleMessage(ctx context.Context, stream, dlStream string, raw goredis.XMessage, handler Handler, knownRetryCount int64) {
	msg, parseErr := parseMessage(raw)
	if parseErr != nil {
		c.deadLetterInvalidMessage(ctx, stream, dlStream, raw, parseErr)
		return
	}
	if err := ValidateMessage(msg, c.maxPayloadSize); err != nil {
		c.deadLetterInvalidMessage(ctx, stream, dlStream, raw, err)
		return
	}

	// Give the handler a shutdown-aware context: derive from the parent
	// context with a grace period so in-flight handlers can complete when
	// shutdown is signaled, rather than being killed immediately.
	var handlerCtx context.Context
	var handlerCancel context.CancelFunc
	if ctx.Err() != nil {
		// Parent already cancelled — detach cancellation but retain context values.
		handlerCtx, handlerCancel = streamDetachedTimeout(ctx, handlerShutdownTimeout)
	} else {
		handlerCtx, handlerCancel = context.WithTimeout(ctx, handlerShutdownTimeout)
	}
	defer handlerCancel()

	start := time.Now()
	err := c.callHandler(handlerCtx, handler, msg)
	duration := time.Since(start)

	c.metrics.processingDuration.WithLabelValues(streamMetricLabel(stream), groupMetricLabel(c.group)).Observe(duration.Seconds())

	// Use a detached context for post-handler operations (ACK, dead-letter).
	// The handler may have cancelled the parent context, but these operations
	// must still succeed to avoid message duplication or loss while preserving
	// context values for tracing/logging/tenant-aware stores.
	//
	// Note: there is a small crash window between XADD (dead-letter write)
	// and XACK. If the process crashes in that window, the message will exist
	// in both the source stream (pending) and the dead-letter stream. This is
	// why handlers MUST be idempotent (see Consume godoc and doc.go).
	ackCtx, ackCancel := streamDetachedTimeout(ctx, ackTimeout)
	defer ackCancel()

	if err == nil {
		if ackErr := c.client.XAck(ackCtx, stream, c.group, raw.ID).Err(); ackErr != nil {
			c.logger.Error("failed to ACK message",
				redact.String("stream", stream),
				redact.String("redis_id", raw.ID),
				redact.Error(ackErr),
			)
		}
		c.metrics.messagesConsumed.WithLabelValues(streamMetricLabel(stream), groupMetricLabel(c.group)).Inc()
		return
	}

	c.metrics.messagesFailed.WithLabelValues(streamMetricLabel(stream), groupMetricLabel(c.group)).Inc()

	// Check if the error is permanent (no retry will help).
	if apperror.IsPermanent(err) {
		c.logger.Error("permanent error, dead-lettering message",
			redact.String("stream", stream),
			redact.String("redis_id", raw.ID),
			redact.String("msg_id", msg.ID),
			redact.Error(err),
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

func (c *Consumer) deadLetterInvalidMessage(ctx context.Context, stream, dlStream string, raw goredis.XMessage, err error) {
	c.logger.Error("invalid stream message, dead-lettering",
		redact.String("stream", stream),
		redact.String("redis_id", raw.ID),
		redact.Error(err),
	)
	c.metrics.messagesFailed.WithLabelValues(streamMetricLabel(stream), groupMetricLabel(c.group)).Inc()

	ackCtx, ackCancel := streamDetachedTimeout(ctx, ackTimeout)
	defer ackCancel()
	c.deadLetter(ackCtx, stream, dlStream, raw, "invalid_message")
}

func (c *Consumer) callHandler(ctx context.Context, handler Handler, msg Message) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("redisstream: handler panic: %s", redact.PanicValue(rec))
		}
	}()
	return handler(ctx, msg.Clone())
}

// handleRetryOrDeadLetter checks the delivery count and either dead-letters
// the message or leaves it pending for retry.
func (c *Consumer) handleRetryOrDeadLetter(ackCtx context.Context, stream, dlStream string, raw goredis.XMessage, msg Message, deliveryCount int64, err error) {
	if c.maxRetries > 0 && deliveryCount > c.maxRetries {
		c.logger.Error("max retries exceeded, dead-lettering message",
			redact.String("stream", stream),
			redact.String("redis_id", raw.ID),
			redact.String("msg_id", msg.ID),
			"delivery_count", deliveryCount,
			"max_retries", c.maxRetries,
			redact.Error(err),
		)
		c.deadLetter(ackCtx, stream, dlStream, raw, "max_retries_exceeded")
		return
	}

	// Leave the message in pending — it will be redelivered on next
	// processPending() call or claimed by another consumer.
	c.logger.Warn("message processing failed, will retry",
		redact.String("stream", stream),
		redact.String("redis_id", raw.ID),
		redact.String("msg_id", msg.ID),
		"delivery_count", deliveryCount,
		"max_retries", c.maxRetries,
		redact.Error(err),
	)
}
