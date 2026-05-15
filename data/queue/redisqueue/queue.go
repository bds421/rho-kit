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

	kitqueue "github.com/bds421/rho-kit/data/v2/queue"
	"github.com/bds421/rho-kit/infra/redis/v2"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// messageIDPattern bounds Message.ID to a safe character set: ASCII letters,
// digits, hyphen, and underscore. UUIDs (with hyphens), ULIDs, and hex IDs
// all fit. The set is deliberately strict so the value is safe as a Lua
// argument, in log lines, in metric labels, and inside JSON without any
// escaping concerns. 1..255 bytes; non-empty and bounded.
var messageIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)

var newQueueConsumerID = uuid.NewV7

// validateMessageID enforces messageIDPattern. Returned as an error so
// callers (Enqueue, EnqueueBatch) can surface the failure without panicking.
func validateMessageID(id string) error {
	if !messageIDPattern.MatchString(id) {
		return fmt.Errorf("%w: message ID must match %s", kitqueue.ErrInvalidMessage, messageIDPattern)
	}
	return nil
}

func validateMessage(msg Message, maxPayloadSize int) error {
	if err := kitqueue.ValidateMessage(kitqueue.Message{
		ID:      msg.ID,
		Type:    msg.Type,
		Payload: msg.Payload,
	}, maxPayloadSize); err != nil {
		return err
	}
	return validateMessageID(msg.ID)
}

const (
	// defaultDeadLetterMaxLen is the approximate maximum number of entries
	// in a dead-letter queue. 0 would mean no limit.
	defaultDeadLetterMaxLen int64 = 10000

	// defaultMaxPayloadSize is the default max message size (1 MiB).
	// Override with WithMaxMessageBytes. Set to 0 to disable the limit.
	defaultMaxPayloadSize = kitqueue.DefaultMaxPayloadBytes

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
	if err := kitqueue.ValidateMessage(kitqueue.Message{Type: msgType}, 0); err != nil {
		return Message{}, err
	}
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

// Clone returns a copy of m with its mutable payload detached.
func (m Message) Clone() Message {
	if m.Payload != nil {
		m.Payload = append(m.Payload[:0:0], m.Payload...)
	}
	return m
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
	dlqDepth             *prometheus.GaugeVec
}

// MetricsOption configures the redisqueue metric constructor.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for queue
// metrics. Unset defaults to [prometheus.DefaultRegisterer]; passing
// nil panics.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("redisqueue: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers queue metrics. Pass [WithRegisterer]
// to use a non-default registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("redisqueue: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

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
		dlqDepth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "dlq_depth",
				Help:      "Number of messages currently in the dead-letter queue. Polled alongside queue_depth so operators can alert on a growing DLQ without standing up a separate poller.",
			},
			[]string{"queue"},
		),
	}

	m.messagesEnqueued = promutil.MustRegisterOrGet(reg, m.messagesEnqueued)
	m.messagesProcessed = promutil.MustRegisterOrGet(reg, m.messagesProcessed)
	m.messagesFailed = promutil.MustRegisterOrGet(reg, m.messagesFailed)
	m.messagesDeadLettered = promutil.MustRegisterOrGet(reg, m.messagesDeadLettered)
	m.processingDuration = promutil.MustRegisterOrGet(reg, m.processingDuration)
	m.messagesRetried = promutil.MustRegisterOrGet(reg, m.messagesRetried)
	m.ackNotFound = promutil.MustRegisterOrGet(reg, m.ackNotFound)
	m.processingDepth = promutil.MustRegisterOrGet(reg, m.processingDepth)
	m.queueDepth = promutil.MustRegisterOrGet(reg, m.queueDepth)
	m.dlqDepth = promutil.MustRegisterOrGet(reg, m.dlqDepth)

	return m
}

var defaultMetrics = sync.OnceValue(func() *Metrics { return NewMetrics() })

func queueMetricLabel(queue string) string {
	return promutil.OpaqueLabelValue("queue", queue)
}

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
// contents to the main queue.
//
// Concurrency: Enqueue and Stats are safe for concurrent use. Process
// is single-goroutine per queue name — calling Process concurrently on
// the same queue will panic; the single-owner contract prevents
// duplication amplification during crash recovery.
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
// The duration must be positive.
func WithBlockTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("redisqueue: WithBlockTimeout requires a positive duration")
	}
	return func(q *Queue) {
		q.blockTimeout = d
	}
}

// WithMaxRetries sets the maximum processing attempts before dead-lettering.
// Negative values panic. Zero dead-letters on the first handler error.
func WithMaxRetries(n int) Option {
	if n < 0 {
		panic("redisqueue: WithMaxRetries requires n >= 0")
	}
	return func(q *Queue) {
		q.maxRetries = n
	}
}

// WithProcessingQueue overrides the default processing queue suffix.
// The final queue name will be "{queue}:{name}" (default suffix: "processing").
// Panics if name is invalid.
func WithProcessingQueue(name string) Option {
	return func(q *Queue) {
		if err := redis.ValidateName(name, "processing queue"); err != nil {
			panic("redisqueue: invalid processing queue name")
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
			panic("redisqueue: invalid dead-letter queue name")
		}
		q.deadLetterQueue = name
	}
}

// WithDeadLetterMaxLen sets the maximum number of entries in the dead-letter
// queue. When exceeded, the oldest entries are trimmed. 0 means no limit.
// Negative values panic. Default is 10000.
func WithDeadLetterMaxLen(n int64) Option {
	if n < 0 {
		panic("redisqueue: WithDeadLetterMaxLen requires n >= 0")
	}
	return func(q *Queue) {
		q.deadLetterMax = n
	}
}

// WithMaxMessageBytes sets the maximum message size in bytes. Messages exceeding
// this limit are rejected at enqueue time. Default is 1 MiB. Set to 0 to
// disable the limit entirely. Negative values panic.
func WithMaxMessageBytes(n int) Option {
	if n < 0 {
		panic("redisqueue: WithMaxMessageBytes requires n >= 0")
	}
	return func(q *Queue) {
		q.maxPayloadSize = n
	}
}

// WithMetricsRegisterer sets the Prometheus registerer for queue
// metrics. If not set, prometheus.DefaultRegisterer is used. Replaces
// the v1 WithRegisterer spelling so it no longer collides with the
// metrics-level option of the same name.
func WithMetricsRegisterer(reg prometheus.Registerer) Option {
	return func(q *Queue) {
		if reg == nil {
			q.metrics = NewMetrics()
			return
		}
		q.metrics = NewMetrics(WithRegisterer(reg))
	}
}

// WithConsumerID overrides the auto-generated consumer ID. Useful for
// deterministic tests and for operators that want to give pods stable
// identities (e.g. derived from the pod name) so abandoned processing
// lists can be identified after a permanent crash.
//
// FR-060 [MED]: panics on IDs longer than [maxConsumerIDLen] or
// containing characters outside [A-Za-z0-9_-]. Long IDs inflate every
// processing-list key; ':' and other delimiters confuse the reaper's
// suffix-stripping logic.
func WithConsumerID(id string) Option {
	if id != "" && !validConsumerID(id) {
		panic("redisqueue: WithConsumerID requires a safe bounded token")
	}
	return func(q *Queue) {
		if id != "" {
			q.consumerID = id
		}
	}
}

const maxConsumerIDLen = 64

// validConsumerID enforces the FR-060 character set + length cap.
func validConsumerID(id string) bool {
	if id == "" || len(id) > maxConsumerIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < 'A' || c > 'Z') &&
			(c < 'a' || c > 'z') &&
			(c < '0' || c > '9') &&
			c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// WithHeartbeatTTL sets the lifetime of the per-consumer heartbeat key
// used by recovery to detect dead consumers. Default is 60s. The duration
// must be positive. The configured interval must be at most TTL/2 so a
// single missed refresh does not immediately expire the key; [NewQueue]
// panics otherwise.
func WithHeartbeatTTL(d time.Duration) Option {
	if d <= 0 {
		panic("redisqueue: WithHeartbeatTTL requires a positive duration")
	}
	return func(q *Queue) {
		q.heartbeatTTL = d
	}
}

// WithHeartbeatInterval sets how often the Process loop refreshes its
// heartbeat key. Default is 20s. The duration must be positive. Must be at
// most TTL/2 (see [WithHeartbeatTTL]); [NewQueue] panics otherwise.
func WithHeartbeatInterval(d time.Duration) Option {
	if d <= 0 {
		panic("redisqueue: WithHeartbeatInterval requires a positive duration")
	}
	return func(q *Queue) {
		q.heartbeatInterval = d
	}
}

// WithoutRecovery opts out of automatic reclaim of stranded processing
// lists left behind by dead consumers (detected via missing heartbeat).
// Default Queue behaviour (no option) runs recovery; jobs claimed by a
// consumer that crashed before deleting them re-enter the pending list
// for another worker.
//
// Use this option only when an external reaper handles recovery or
// the deployment guarantees consumers always shut down cleanly. With
// no recovery configured, a hard consumer crash silently strands
// jobs.
//
// Replaces the v1 WithRecoveryEnabled(bool) form so the durability
// opt-out is a typed named intent rather than a one-token bool flip.
func WithoutRecovery() Option {
	return func(q *Queue) {
		q.recoveryEnabled = false
	}
}

// NewQueue creates a LIST-based queue. Panics if client is nil, options are
// invalid, or the auto-generated consumer ID cannot be generated. A
// consumer-ID generation failure is pathological — it means crypto/rand
// returned an error, which only happens on a broken OS — so panicking matches
// the kit-wide convention for invariants the runtime cannot recover from.
func NewQueue(client goredis.UniversalClient, opts ...Option) *Queue {
	if client == nil {
		panic("redisqueue: NewQueue requires a non-nil Redis client")
	}
	q := &Queue{
		client:            client,
		logger:            slog.Default(),
		blockTimeout:      5 * time.Second,
		maxRetries:        5,
		deadLetterMax:     defaultDeadLetterMaxLen,
		maxPayloadSize:    defaultMaxPayloadSize,
		heartbeatTTL:      defaultHeartbeatTTL,
		heartbeatInterval: defaultHeartbeatInterval,
		recoveryEnabled:   true,
		metrics:           defaultMetrics(),
		activeQueues:      make(map[string]bool),
	}
	for _, o := range opts {
		if o == nil {
			panic("redisqueue: NewQueue option must not be nil")
		}
		o(q)
	}
	if q.consumerID == "" {
		consumerID, err := newQueueConsumerID()
		if err != nil {
			panic("redisqueue: NewQueue: generate consumer ID: " + err.Error())
		}
		q.consumerID = consumerID.String()
	}

	// Heartbeat-ratio guard: enforce interval <= TTL/2 so one missed refresh
	// does not immediately expire the key and let a peer's reaper reclaim
	// this consumer's in-flight messages mid-handler. interval >= TTL is an
	// outright bug; interval > TTL/2 is the unsafe edge where a single
	// transient Redis stall causes false-dead reclaim under normal load.
	if q.heartbeatTTL <= 0 {
		panic("redisqueue: heartbeat TTL must be positive")
	}
	if q.heartbeatInterval <= 0 {
		panic("redisqueue: heartbeat interval must be positive")
	}
	if q.heartbeatInterval > q.heartbeatTTL/2 {
		panic("redisqueue: heartbeat interval must be at most TTL/2; a higher ratio lets a single missed refresh expire the key and trigger false-dead reclaim")
	}

	return q
}

func (q *Queue) ready() error {
	if q == nil ||
		q.client == nil ||
		q.logger == nil ||
		q.metrics == nil ||
		!validConsumerID(q.consumerID) ||
		q.blockTimeout <= 0 ||
		q.maxRetries < 0 ||
		q.deadLetterMax < 0 ||
		q.maxPayloadSize < 0 ||
		q.heartbeatTTL <= 0 ||
		q.heartbeatInterval <= 0 ||
		q.heartbeatInterval > q.heartbeatTTL/2 ||
		q.activeQueues == nil {
		return kitqueue.ErrInvalidQueue
	}
	return nil
}

// ConsumerID returns the unique identifier for this Queue instance. Stable
// for the lifetime of the Queue; included in processing-list naming so
// crash recovery scopes itself to this consumer's in-flight messages.
func (q *Queue) ConsumerID() string {
	if q == nil {
		return ""
	}
	return q.consumerID
}

// Enqueue adds a message to the queue (LPUSH — left side).
// Returns an error if the serialized message exceeds the configured max
// payload size or if Message.ID is not a safe identifier (see
// [validateMessageID]). Caller-supplied IDs are constrained because the ack
// path uses the ID as an exact-match key inside a Lua script; allowing
// arbitrary bytes (quotes, control chars) leaves the door open for ack
// misses and processing-list corruption.
func (q *Queue) Enqueue(ctx context.Context, queue string, msg Message) error {
	if err := q.ready(); err != nil {
		return err
	}
	if err := redis.ValidateName(queue, "queue"); err != nil {
		return err
	}
	if err := validateMessage(msg, q.maxPayloadSize); err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	if q.maxPayloadSize > 0 && len(data) > q.maxPayloadSize {
		return &kitqueue.MessageTooLargeError{Size: len(data), Limit: q.maxPayloadSize}
	}
	if err := q.client.LPush(ctx, queue, data).Err(); err != nil {
		return fmt.Errorf("lpush: %w", err)
	}
	q.metrics.messagesEnqueued.WithLabelValues(queueMetricLabel(queue)).Inc()
	return nil
}

// EnqueueBatch adds multiple messages in a single pipeline.
//
// Note: Redis pipelines are not atomic. If the pipeline partially fails,
// some messages may have been enqueued. Callers should treat a pipeline
// error as "unknown state" and rely on idempotent handlers for deduplication.
func (q *Queue) EnqueueBatch(ctx context.Context, queue string, msgs []Message) error {
	if err := q.ready(); err != nil {
		return err
	}
	if err := redis.ValidateName(queue, "queue"); err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}
	if len(msgs) > kitqueue.MaxBatchMessages {
		return kitqueue.ErrBatchTooLarge
	}

	for i, msg := range msgs {
		if err := validateMessage(msg, q.maxPayloadSize); err != nil {
			return fmt.Errorf("message [%d]: %w", i, err)
		}
	}

	pipe := q.client.Pipeline()
	for i, msg := range msgs {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal message [%d]: %w", i, err)
		}
		if q.maxPayloadSize > 0 && len(data) > q.maxPayloadSize {
			return fmt.Errorf("message [%d]: %w", i, &kitqueue.MessageTooLargeError{Size: len(data), Limit: q.maxPayloadSize})
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
			q.metrics.messagesEnqueued.WithLabelValues(queueMetricLabel(queue)).Add(float64(succeeded))
		}
		return fmt.Errorf("pipeline exec: %w", err)
	}
	q.metrics.messagesEnqueued.WithLabelValues(queueMetricLabel(queue)).Add(float64(len(msgs)))
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
	if err := q.ready(); err != nil {
		panic("redisqueue: queue is invalid")
	}
	if err := redis.ValidateName(queue, "queue"); err != nil {
		panic("redisqueue: invalid queue name")
	}
	if handler == nil {
		panic("redisqueue: Queue.Process requires a non-nil handler")
	}

	// Guard against concurrent Process on the same queue name.
	q.activeQueuesMu.Lock()
	if q.activeQueues[queue] {
		q.activeQueuesMu.Unlock()
		panic("redisqueue: queue already has an active Process goroutine")
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

	// processCtx is what the main Process loop and recovery operations run
	// under. A permanent heartbeat failure cancels it so the local consumer
	// stops pulling new work the moment a peer reaper is poised to reclaim
	// our processing list — preventing duplicate dispatch.
	processCtx, processCancel := context.WithCancel(ctx)
	defer processCancel()

	bgWG := q.startBackgroundLoops(bgCtx, processCancel, queue, heartbeatKey, heartbeatPrefix, processingPrefix, processingQ, deadQ, handler)
	defer bgWG.Wait()

	redis.RunWithBackoff(processCtx, q.logger, "queue processor", func(ctx context.Context) error {
		return q.processOnce(ctx, queue, processingQ, deadQ, handler)
	})
}

// startBackgroundLoops launches the heartbeat refresh goroutine and (when
// recovery is enabled) the dead-consumer reaper goroutine. Both stop when
// ctx is cancelled. The cancelProcess callback is invoked when the
// heartbeat permanently fails — cancelling the local Process loop avoids
// double-dispatch when a peer reaper is about to reclaim our list.
// Returns a WaitGroup so Process can wait for them on shutdown — the
// heartbeat key is intentionally NOT deleted at shutdown so that any
// in-flight reclaim by a peer fails closed.
func (q *Queue) startBackgroundLoops(
	ctx context.Context,
	cancelProcess context.CancelFunc,
	queue, heartbeatKey, heartbeatPrefix, processingPrefix, processingQ, deadQ string,
	handler Handler,
) *sync.WaitGroup {
	wg := &sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		q.heartbeatLoop(ctx, heartbeatKey, cancelProcess)
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
	q.updateProcessingDepth(ctx, queue, processingQ, deadQ)

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
				q.updateProcessingDepth(ctx, queue, processingQ, deadQ)
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("blmove: %w", err)
		}

		q.handleMessage(ctx, result, processingQ, queue, deadQ, handler)
		iter++
	}
}

// Len returns the number of messages in the queue.
func (q *Queue) Len(ctx context.Context, queue string) (int64, error) {
	if err := q.ready(); err != nil {
		return 0, err
	}
	if err := redis.ValidateName(queue, "queue"); err != nil {
		return 0, err
	}
	n, err := q.client.LLen(ctx, queue).Result()
	if err != nil {
		return 0, fmt.Errorf("llen: %w", err)
	}
	return n, nil
}
