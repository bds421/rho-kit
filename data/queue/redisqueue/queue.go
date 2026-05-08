package redisqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/infra/redis/v2"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// messageIDPattern bounds Message.ID to a safe character set: ASCII letters,
// digits, hyphen, and underscore. UUIDs (with hyphens), ULIDs, and hex IDs
// all fit. The set is deliberately strict so the value is safe as a Lua
// argument, in log lines, in metric labels, and inside JSON without any
// escaping concerns. 1..255 bytes; non-empty and bounded.
var messageIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)

// validateMessageID enforces messageIDPattern. Returned as an error so
// callers (Enqueue, EnqueueBatch) can surface the failure without panicking.
func validateMessageID(id string) error {
	if !messageIDPattern.MatchString(id) {
		return fmt.Errorf("message ID must match %s (got %q)", messageIDPattern, id)
	}
	return nil
}

const (
	// defaultDeadLetterMaxLen is the approximate maximum number of entries
	// in a dead-letter queue. 0 would mean no limit.
	defaultDeadLetterMaxLen int64 = 10000

	// defaultMaxPayloadSize is the default max message size (1 MiB).
	// Override with WithMaxPayloadSize. Set to 0 to disable the limit.
	// Kept in sync with stream.defaultStreamMaxPayloadSize.
	defaultMaxPayloadSize = 1 << 20 // 1 MiB

	// defaultHeartbeatTTL is how long an active consumer's heartbeat key
	// lives in Redis before expiring. Recovery treats a missing heartbeat as
	// proof that the owning consumer is dead and its processing list can be
	// reclaimed safely.
	defaultHeartbeatTTL = 60 * time.Second

	// defaultHeartbeatInterval is how often Process refreshes its heartbeat
	// key. Set to TTL/3 to tolerate transient failures and clock skew.
	defaultHeartbeatInterval = 20 * time.Second

	// heartbeatSuffix is the key suffix used by the heartbeat scheme.
	heartbeatSuffix = "heartbeat"
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
	ackNotFound          *prometheus.CounterVec
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
		ackNotFound: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "ack_not_found_total",
				Help:      "Total successful handler acks where the processing-list entry was not found. A non-zero value signals processing-list corruption (e.g. concurrent reaping or a malformed entry).",
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
	promutil.RegisterCollector(reg, m.ackNotFound)
	promutil.RegisterCollector(reg, m.processingDepth)
	promutil.RegisterCollector(reg, m.queueDepth)

	return m
}

var defaultMetrics = NewMetrics(nil)

// Queue provides reliable LIST-based FIFO queuing using BLMOVE.
//
// Pattern: messages are atomically popped from the main queue and pushed
// to a per-consumer processing list. On success, the message is removed
// from the processing list by message-ID (via a Lua script that finds and
// tombstones the entry — payload-equality LREM has a duplicate-payload
// race documented in the audit). On failure/crash, messages remaining in
// THIS consumer's processing list are recovered at next startup.
//
// Each Queue instance generates a per-process consumer ID (UUID v7) that
// scopes its processing list. Rolling deploys and horizontal scale-out are
// safe because a fresh consumer never recovers messages from a sibling
// consumer's in-flight list — the previous shared "{queue}:processing"
// design double-processed every in-flight message on every restart.
//
// Limitation: this consumer-scoped recovery model does NOT reclaim
// messages from a permanently-dead consumer (e.g. a pod that OOM'd and
// will not return). Operators with that requirement should run a periodic
// reaper that inspects abandoned per-consumer lists and re-enqueues their
// contents to the main queue. A built-in reaper based on heartbeat keys
// is tracked as a v2.1+ follow-up in docs/audit/ROADMAP.md.
//
// Only one Process goroutine per queue name is allowed. Calling Process
// concurrently on the same queue will panic — this prevents duplication
// amplification during crash recovery.
type Queue struct {
	client goredis.UniversalClient
	logger *slog.Logger

	// processingQueue is the temporary holding queue for in-flight messages.
	// Defaults to "{queue}:processing:{consumerID}".
	processingQueue string

	// deadLetterQueue stores messages that exceeded max retries.
	// Defaults to "{queue}:dead".
	deadLetterQueue string

	// consumerID uniquely identifies this Queue instance for processing-list
	// scoping. Generated at construction; visible in logs.
	consumerID string

	blockTimeout   time.Duration
	maxRetries     int
	deadLetterMax  int64 // max entries in dead-letter queue; 0 = no limit
	maxPayloadSize int   // max message size in bytes; 0 = no limit

	// heartbeatTTL is the lifetime of the per-consumer heartbeat key. Recovery
	// considers a consumer dead when its heartbeat key has expired.
	heartbeatTTL time.Duration

	// heartbeatInterval is how often the Process loop refreshes the heartbeat key.
	heartbeatInterval time.Duration

	// recoveryEnabled toggles startup-and-periodic reclaim of stranded
	// processing lists from dead consumers. Default true.
	recoveryEnabled bool

	// reapInitialDelay overrides the default reaper warm-up delay. Used by
	// tests to avoid waiting the full default. Zero means use the default.
	reapInitialDelay time.Duration

	metrics *Metrics

	// activeQueues tracks which queue names have an active Process goroutine
	// to prevent concurrent processing on the same queue.
	activeQueuesMu sync.Mutex
	activeQueues   map[string]bool
}

// Option configures a Queue.
type Option func(*Queue)

// WithLogger sets the logger. A nil logger is normalized to [slog.Default]
// so the queue never holds a nil slog.Logger.
func WithLogger(l *slog.Logger) Option {
	return func(q *Queue) {
		if l == nil {
			q.logger = slog.Default()
			return
		}
		q.logger = l
	}
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

// WithConsumerID overrides the auto-generated consumer ID. Useful for
// deterministic tests and for operators that want to give pods stable
// identities (e.g. derived from the pod name) so abandoned processing
// lists can be identified after a permanent crash.
func WithConsumerID(id string) Option {
	return func(q *Queue) {
		if id != "" {
			q.consumerID = id
		}
	}
}

// WithHeartbeatTTL sets the lifetime of the per-consumer heartbeat key
// used by recovery to detect dead consumers. Values <= 0 are ignored.
// Default is 60s. The configured interval must be at most TTL/2 so a
// single missed refresh does not immediately expire the key — [NewQueue]
// panics otherwise.
func WithHeartbeatTTL(d time.Duration) Option {
	return func(q *Queue) {
		if d > 0 {
			q.heartbeatTTL = d
		}
	}
}

// WithHeartbeatInterval sets how often the Process loop refreshes its
// heartbeat key. Values <= 0 are ignored. Default is 20s. Must be at
// most TTL/2 (see [WithHeartbeatTTL]); [NewQueue] panics otherwise.
func WithHeartbeatInterval(d time.Duration) Option {
	return func(q *Queue) {
		if d > 0 {
			q.heartbeatInterval = d
		}
	}
}

// WithRecoveryEnabled toggles automatic reclaim of stranded processing
// lists left behind by dead consumers (detected via missing heartbeat).
// Default true. Disable only if you have an external reaper or know that
// consumers always shut down cleanly.
func WithRecoveryEnabled(enabled bool) Option {
	return func(q *Queue) {
		q.recoveryEnabled = enabled
	}
}

// NewQueue creates a LIST-based queue. Panics if client is nil — a miswired
// queue would otherwise dereference nil on the first Push or Pop.
func NewQueue(client goredis.UniversalClient, opts ...Option) *Queue {
	if client == nil {
		panic("redisqueue: NewQueue requires a non-nil Redis client")
	}
	consumerID, err := uuid.NewV7()
	if err != nil {
		panic("redis: failed to generate consumer ID: " + err.Error())
	}
	q := &Queue{
		client:            client,
		logger:            slog.Default(),
		consumerID:        consumerID.String(),
		blockTimeout:      5 * time.Second,
		maxRetries:        5,
		deadLetterMax:     defaultDeadLetterMaxLen,
		maxPayloadSize:    defaultMaxPayloadSize,
		heartbeatTTL:      defaultHeartbeatTTL,
		heartbeatInterval: defaultHeartbeatInterval,
		recoveryEnabled:   true,
		metrics:           defaultMetrics,
		activeQueues:      make(map[string]bool),
	}
	for _, o := range opts {
		o(q)
	}

	// Heartbeat-ratio guard: enforce interval <= TTL/2 so one missed refresh
	// does not immediately expire the key and let a peer's reaper reclaim
	// this consumer's in-flight messages mid-handler. interval >= TTL is an
	// outright bug; interval > TTL/2 is the unsafe edge where a single
	// transient Redis stall causes false-dead reclaim under normal load.
	if q.heartbeatTTL <= 0 {
		panic(fmt.Sprintf("redisqueue: heartbeat TTL must be positive (got %s)", q.heartbeatTTL))
	}
	if q.heartbeatInterval <= 0 {
		panic(fmt.Sprintf("redisqueue: heartbeat interval must be positive (got %s)", q.heartbeatInterval))
	}
	if q.heartbeatInterval*2 > q.heartbeatTTL {
		panic(fmt.Sprintf(
			"redisqueue: heartbeat interval %s must be at most TTL/2 (TTL=%s); a higher ratio lets a single missed refresh expire the key and trigger false-dead reclaim",
			q.heartbeatInterval, q.heartbeatTTL,
		))
	}

	return q
}

// ConsumerID returns the unique identifier for this Queue instance. Stable
// for the lifetime of the Queue; included in processing-list naming so
// crash recovery scopes itself to this consumer's in-flight messages.
func (q *Queue) ConsumerID() string { return q.consumerID }

// Enqueue adds a message to the queue (LPUSH — left side).
// Returns an error if the serialized message exceeds the configured max
// payload size or if Message.ID is not a safe identifier (see
// [validateMessageID]). Caller-supplied IDs are constrained because the ack
// path uses the ID as an exact-match key inside a Lua script; allowing
// arbitrary bytes (quotes, control chars) leaves the door open for ack
// misses and processing-list corruption.
func (q *Queue) Enqueue(ctx context.Context, queue string, msg Message) error {
	if err := redis.ValidateName(queue, "queue"); err != nil {
		return err
	}
	if err := validateMessageID(msg.ID); err != nil {
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

	for i, msg := range msgs {
		if err := validateMessageID(msg.ID); err != nil {
			return fmt.Errorf("message [%d] %w", i, err)
		}
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
// Panics if queue name is empty or handler is nil (programming errors —
// fail fast at startup rather than on the first message).
func (q *Queue) Process(ctx context.Context, queue string, handler Handler) {
	if err := redis.ValidateName(queue, "queue"); err != nil {
		panic("redis: " + err.Error())
	}
	if handler == nil {
		panic("redisqueue: Queue.Process requires a non-nil handler")
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
	//
	// CRITICAL: the processing list is scoped per-consumer (suffix
	// :{consumerID}) so a fresh consumer never recovers messages claimed by
	// a sibling consumer that is still alive. The previous shared
	// "{queue}:processing" design double-processed every in-flight message
	// on every rolling-deploy restart.
	processingSuffix := "processing"
	if q.processingQueue != "" {
		processingSuffix = q.processingQueue
	}
	processingQ := queue + ":" + processingSuffix + ":" + q.consumerID
	deadQ := queue + ":dead"
	if q.deadLetterQueue != "" {
		deadQ = queue + ":" + q.deadLetterQueue
	}
	processingPrefix := queue + ":" + processingSuffix + ":"
	heartbeatKey := queue + ":" + heartbeatSuffix + ":" + q.consumerID
	heartbeatPrefix := queue + ":" + heartbeatSuffix + ":"

	// Write our heartbeat synchronously before starting recovery so that any
	// peer running recovery against us sees us as alive. Errors are logged
	// inside refreshHeartbeat; we keep going either way so a transient
	// blip on first refresh doesn't block process startup.
	_ = q.refreshHeartbeat(ctx, heartbeatKey)

	bgCtx, bgCancel := context.WithCancel(ctx)
	defer bgCancel()

	bgWG := q.startBackgroundLoops(bgCtx, queue, heartbeatKey, heartbeatPrefix, processingPrefix, processingQ, deadQ, handler)
	defer bgWG.Wait()

	redis.RunWithBackoff(ctx, q.logger, "queue processor", func(ctx context.Context) error {
		return q.processOnce(ctx, queue, processingQ, deadQ, handler)
	})
}

// startBackgroundLoops launches the heartbeat refresh goroutine and (when
// recovery is enabled) the dead-consumer reaper goroutine. Both stop when
// ctx is cancelled. Returns a WaitGroup so Process can wait for them on
// shutdown — the heartbeat key is intentionally NOT deleted at shutdown so
// that any in-flight reclaim by a peer fails closed (peer sees the key still
// alive, defers deletion of the processing list to the next reaper pass).
func (q *Queue) startBackgroundLoops(
	ctx context.Context,
	queue, heartbeatKey, heartbeatPrefix, processingPrefix, processingQ, deadQ string,
	handler Handler,
) *sync.WaitGroup {
	wg := &sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		q.heartbeatLoop(ctx, heartbeatKey)
	}()

	if q.recoveryEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.reaperLoop(ctx, queue, heartbeatPrefix, processingPrefix, processingQ, deadQ, handler)
		}()
	}

	return wg
}

func (q *Queue) processOnce(ctx context.Context, queue, processingQ, deadQ string, handler Handler) error {
	// First, recover any messages left in the processing queue (crash recovery).
	if _, err := q.recoverProcessing(ctx, processingQ, queue, deadQ, handler); err != nil {
		return err
	}

	// Initial depth snapshot before entering the processing loop.
	q.updateProcessingDepth(ctx, queue, processingQ)

	// recoveryInterval interleaves recovery passes with new-message reads so
	// a permanent processing-list backlog doesn't head-of-line-block normal
	// traffic. Every recoveryInterval BLMove iterations we run another
	// bounded recoverProcessing pass; the per-pass batch (10) and the
	// interval (10) together amortise an N-entry backlog over N normal
	// messages. This mirrors the data/stream consumer's claimMinIdle cadence.
	const recoveryInterval = 10
	iter := 0

	for {
		if iter > 0 && iter%recoveryInterval == 0 {
			if _, err := q.recoverProcessing(ctx, processingQ, queue, deadQ, handler); err != nil {
				return err
			}
		}

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

		q.handleMessage(ctx, result, processingQ, queue, deadQ, handler)
		iter++
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
