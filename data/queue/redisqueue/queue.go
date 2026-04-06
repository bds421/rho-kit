package redisqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/infra/redis"
	"github.com/bds421/rho-kit/observability/promutil"
)

const (
	// defaultDeadLetterMaxLen is the approximate maximum number of entries
	// in a dead-letter queue. 0 would mean no limit.
	defaultDeadLetterMaxLen int64 = 10000

	// defaultMaxPayloadSize is the default max message size (1 MiB).
	// Override with WithMaxPayloadSize. Set to 0 to disable the limit.
	// Kept in sync with stream.defaultStreamMaxPayloadSize.
	defaultMaxPayloadSize = 1 << 20 // 1 MiB
)

// Message is a message stored in a Redis LIST-based queue.
type Message struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
	Attempt   int             `json:"attempt"`
}

// NewMessage creates a Message with a UUID v7 ID and current timestamp.
func NewMessage(msgType string, payload any) (Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("marshal payload: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Message{}, fmt.Errorf("generate message ID: %w", err)
	}
	return Message{
		ID:        id.String(),
		Type:      msgType,
		Payload:   data,
		Timestamp: time.Now().UTC(),
		Attempt:   1,
	}, nil
}

// Handler processes a single queue message.
type Handler func(ctx context.Context, msg Message) error

// Metrics holds Prometheus collectors for queue monitoring.
type Metrics struct {
	messagesEnqueued     *prometheus.CounterVec
	messagesProcessed    *prometheus.CounterVec
	messagesFailed       *prometheus.CounterVec
	messagesDeadLettered *prometheus.CounterVec
	processingDuration   *prometheus.HistogramVec
	messagesRetried      *prometheus.CounterVec
	processingDepth      *prometheus.GaugeVec
	queueDepth           *prometheus.GaugeVec
}

// NewMetrics creates and registers queue metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &Metrics{
		messagesEnqueued: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "messages_enqueued_total",
				Help:      "Total messages enqueued.",
			},
			[]string{"queue"},
		),
		messagesProcessed: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "messages_processed_total",
				Help:      "Total messages successfully processed.",
			},
			[]string{"queue"},
		),
		messagesFailed: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "messages_failed_total",
				Help:      "Total messages that failed processing.",
			},
			[]string{"queue"},
		),
		messagesDeadLettered: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "messages_dead_lettered_total",
				Help:      "Total messages moved to dead-letter queue.",
			},
			[]string{"queue"},
		),
		processingDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "processing_duration_seconds",
				Help:      "Duration of queue message processing.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"queue"},
		),
		messagesRetried: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "messages_retried_total",
				Help:      "Total messages re-enqueued for retry.",
			},
			[]string{"queue"},
		),
		processingDepth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "processing_depth",
				Help:      "Number of messages currently in the processing queue.",
			},
			[]string{"queue"},
		),
		queueDepth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "queue_depth",
				Help:      "Number of messages waiting in the main queue.",
			},
			[]string{"queue"},
		),
	}

	promutil.RegisterCollector(reg, m.messagesEnqueued)
	promutil.RegisterCollector(reg, m.messagesProcessed)
	promutil.RegisterCollector(reg, m.messagesFailed)
	promutil.RegisterCollector(reg, m.messagesDeadLettered)
	promutil.RegisterCollector(reg, m.processingDuration)
	promutil.RegisterCollector(reg, m.messagesRetried)
	promutil.RegisterCollector(reg, m.processingDepth)
	promutil.RegisterCollector(reg, m.queueDepth)

	return m
}

var defaultMetrics = NewMetrics(nil)

// Queue provides reliable LIST-based FIFO queuing using BLMOVE.
//
// Pattern: messages are atomically popped from the main queue and pushed
// to a processing queue. On success, the message is removed from the
// processing queue. On failure/crash, messages in the processing queue
// are recovered by the claim loop.
//
// Only one Process goroutine per queue name is allowed. Calling Process
// concurrently on the same queue will panic — this prevents duplication
// amplification during crash recovery.
type Queue struct {
	client goredis.UniversalClient
	logger *slog.Logger

	// processingQueue is the temporary holding queue for in-flight messages.
	// Defaults to "{queue}:processing".
	processingQueue string

	// deadLetterQueue stores messages that exceeded max retries.
	// Defaults to "{queue}:dead".
	deadLetterQueue string

	blockTimeout   time.Duration
	maxRetries     int
	deadLetterMax  int64 // max entries in dead-letter queue; 0 = no limit
	maxPayloadSize int   // max message size in bytes; 0 = no limit

	metrics *Metrics

	// activeQueues tracks which queue names have an active Process goroutine
	// to prevent concurrent processing on the same queue.
	activeQueuesMu sync.Mutex
	activeQueues   map[string]bool
}

// Option configures a Queue.
type Option func(*Queue)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option {
	return func(q *Queue) { q.logger = l }
}

// WithBlockTimeout sets how long BLMOVE blocks waiting for messages.
// Values <= 0 are ignored; the default is used instead.
func WithBlockTimeout(d time.Duration) Option {
	return func(q *Queue) {
		if d > 0 {
			q.blockTimeout = d
		}
	}
}

// WithMaxRetries sets the maximum processing attempts before dead-lettering.
// Values < 0 are ignored; the default is used instead.
func WithMaxRetries(n int) Option {
	return func(q *Queue) {
		if n >= 0 {
			q.maxRetries = n
		}
	}
}

// WithProcessingQueue overrides the default processing queue suffix.
// The final queue name will be "{queue}:{name}" (default suffix: "processing").
// Panics if name is invalid.
func WithProcessingQueue(name string) Option {
	return func(q *Queue) {
		if err := redis.ValidateName(name, "processing queue"); err != nil {
			panic("redis: " + err.Error())
		}
		q.processingQueue = name
	}
}

// WithDeadLetterQueue overrides the default dead-letter queue suffix.
// The final queue name will be "{queue}:{name}" (default suffix: "dead").
// Panics if name is invalid.
func WithDeadLetterQueue(name string) Option {
	return func(q *Queue) {
		if err := redis.ValidateName(name, "dead-letter queue"); err != nil {
			panic("redis: " + err.Error())
		}
		q.deadLetterQueue = name
	}
}

// WithDeadLetterMaxLen sets the maximum number of entries in the dead-letter
// queue. When exceeded, the oldest entries are trimmed. 0 means no limit.
// Negative values are ignored. Default is 10000.
func WithDeadLetterMaxLen(n int64) Option {
	return func(q *Queue) {
		if n >= 0 {
			q.deadLetterMax = n
		}
	}
}

// WithMaxPayloadSize sets the maximum message size in bytes. Messages exceeding
// this limit are rejected at enqueue time. Default is 1 MiB. Set to 0 to
// disable the limit entirely (use with caution). Negative values are ignored.
func WithMaxPayloadSize(n int) Option {
	return func(q *Queue) {
		if n >= 0 {
			q.maxPayloadSize = n
		}
	}
}

// WithRegisterer sets the Prometheus registerer for queue metrics.
// If not set, prometheus.DefaultRegisterer is used.
func WithRegisterer(reg prometheus.Registerer) Option {
	return func(q *Queue) {
		q.metrics = NewMetrics(reg)
	}
}

// NewQueue creates a LIST-based queue.
func NewQueue(client goredis.UniversalClient, opts ...Option) *Queue {
	q := &Queue{
		client:         client,
		logger:         slog.Default(),
		blockTimeout:   5 * time.Second,
		maxRetries:     5,
		deadLetterMax:  defaultDeadLetterMaxLen,
		maxPayloadSize: defaultMaxPayloadSize,
		metrics:        defaultMetrics,
		activeQueues:   make(map[string]bool),
	}
	for _, o := range opts {
		o(q)
	}
	return q
}

// Enqueue adds a message to the queue (LPUSH — left side).
// Returns an error if the serialized message exceeds the configured max payload size.
func (q *Queue) Enqueue(ctx context.Context, queue string, msg Message) error {
	if err := redis.ValidateName(queue, "queue"); err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	if q.maxPayloadSize > 0 && len(data) > q.maxPayloadSize {
		return fmt.Errorf("message size %d exceeds max payload size %d", len(data), q.maxPayloadSize)
	}
	if err := q.client.LPush(ctx, queue, data).Err(); err != nil {
		return fmt.Errorf("lpush %s: %w", queue, err)
	}
	q.metrics.messagesEnqueued.WithLabelValues(queue).Inc()
	return nil
}

// EnqueueBatch adds multiple messages in a single pipeline.
//
// Note: Redis pipelines are not atomic. If the pipeline partially fails,
// some messages may have been enqueued. Callers should treat a pipeline
// error as "unknown state" and rely on idempotent handlers for deduplication.
func (q *Queue) EnqueueBatch(ctx context.Context, queue string, msgs []Message) error {
	if err := redis.ValidateName(queue, "queue"); err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}

	pipe := q.client.Pipeline()
	for i, msg := range msgs {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal message [%d]: %w", i, err)
		}
		if q.maxPayloadSize > 0 && len(data) > q.maxPayloadSize {
			return fmt.Errorf("message [%d] size %d exceeds max payload size %d", i, len(data), q.maxPayloadSize)
		}
		pipe.LPush(ctx, queue, data)
	}

	cmds, err := pipe.Exec(ctx)
	if err != nil {
		// Pipeline errors can be partial — count the commands that succeeded.
		var succeeded int
		for _, cmd := range cmds {
			if cmd.Err() == nil {
				succeeded++
			}
		}
		if succeeded > 0 {
			q.metrics.messagesEnqueued.WithLabelValues(queue).Add(float64(succeeded))
		}
		return fmt.Errorf("pipeline exec: %w", err)
	}
	q.metrics.messagesEnqueued.WithLabelValues(queue).Add(float64(len(msgs)))
	return nil
}

// Process reads messages from the queue and dispatches to handler.
// Blocks until ctx is cancelled. Automatically restarts on error.
//
// Important: handlers must be idempotent, as messages may be redelivered
// after crashes (messages are recovered from the processing queue on restart).
//
// Panics if queue name is empty (programming error — fail fast).
func (q *Queue) Process(ctx context.Context, queue string, handler Handler) {
	if err := redis.ValidateName(queue, "queue"); err != nil {
		panic("redis: " + err.Error())
	}

	// Guard against concurrent Process on the same queue name.
	q.activeQueuesMu.Lock()
	if q.activeQueues[queue] {
		q.activeQueuesMu.Unlock()
		panic(fmt.Sprintf("redis: queue %q already has an active Process goroutine", queue))
	}
	q.activeQueues[queue] = true
	q.activeQueuesMu.Unlock()
	defer func() {
		q.activeQueuesMu.Lock()
		delete(q.activeQueues, queue)
		q.activeQueuesMu.Unlock()
	}()

	// Always derive per-queue names to prevent cross-contamination when a
	// single Queue instance processes multiple queue names. Global overrides
	// (WithProcessingQueue/WithDeadLetterQueue) replace the suffix only,
	// keeping the queue name as prefix for consistent key namespacing.
	processingQ := queue + ":processing"
	deadQ := queue + ":dead"
	if q.processingQueue != "" {
		processingQ = queue + ":" + q.processingQueue
	}
	if q.deadLetterQueue != "" {
		deadQ = queue + ":" + q.deadLetterQueue
	}

	redis.RunWithBackoff(ctx, q.logger, "queue processor", func(ctx context.Context) error {
		return q.processOnce(ctx, queue, processingQ, deadQ, handler)
	})
}

func (q *Queue) processOnce(ctx context.Context, queue, processingQ, deadQ string, handler Handler) error {
	// First, recover any messages left in the processing queue (crash recovery).
	if err := q.recoverProcessing(ctx, processingQ, queue, deadQ, handler); err != nil {
		return err
	}

	// Initial depth snapshot before entering the processing loop.
	q.updateProcessingDepth(ctx, queue, processingQ)

	for {
		// BLMOVE atomically pops from queue (right) and pushes to processingQ (left).
		// This ensures messages survive consumer crashes.
		result, err := q.client.BLMove(ctx, queue, processingQ, "RIGHT", "LEFT", q.blockTimeout).Result()
		if err != nil {
			if errors.Is(err, goredis.Nil) {
				// Timeout with no messages — good time to update the depth gauge.
				// This throttles the LLen call to at most once per blockTimeout
				// instead of once per message, reducing Redis round-trips.
				q.updateProcessingDepth(ctx, queue, processingQ)
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("blmove %s: %w", queue, err)
		}

		q.handleMessage(ctx, result, processingQ, queue, deadQ, handler, false)
	}
}

// Len returns the number of messages in the queue.
func (q *Queue) Len(ctx context.Context, queue string) (int64, error) {
	if err := redis.ValidateName(queue, "queue"); err != nil {
		return 0, err
	}
	n, err := q.client.LLen(ctx, queue).Result()
	if err != nil {
		return 0, fmt.Errorf("llen %s: %w", queue, err)
	}
	return n, nil
}
