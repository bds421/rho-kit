package redisqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/core/v2/redact"
	kitqueue "github.com/bds421/rho-kit/data/v2/queue"
	"github.com/bds421/rho-kit/infra/redis/v2"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// messageIDPattern bounds Message.ID to a safe character set: ASCII letters,
// digits, hyphen, and underscore. UUIDs (with hyphens), ULIDs, and hex IDs
// all fit. The set is deliberately strict so the value is safe as an asynq
// TaskID (which must be unique per queue), in log lines, in metric labels,
// and inside JSON without escaping concerns. 1..255 bytes; non-empty and
// bounded.
var messageIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)

// newQueueConsumerID is the package-level hook tests swap to install a
// deterministic consumer ID. Production code reads through this
// indirection rather than calling [id.New] so tests can assert against
// a fixed consumer name without dragging in core/v2/id internals.
var newQueueConsumerID = id.New

// envelopeTaskType is the single asynq task type the kit publishes under.
// Routing happens via Queue (per-tenant queue names) plus the inner
// envelope's Type field; the kit deliberately keeps asynq's type system
// minimal so consumers see one stable handler entry point.
const envelopeTaskType = "rho.envelope"

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
	// defaultMaxPayloadSize is the default max message size (1 MiB).
	// Override with WithMaxMessageBytes. Set to 0 to disable the limit.
	defaultMaxPayloadSize = kitqueue.DefaultMaxPayloadBytes

	// defaultConcurrency is the asynq server concurrency. The kit's pre-v2
	// LIST-based queue was effectively single-consumer per Process call;
	// asynq pulls multiple in parallel by default. We keep "1" as the
	// default so existing handlers do not silently get fan-out, and offer
	// [WithConcurrency] for opt-in throughput.
	defaultConcurrency = 1

	// defaultMaxRetries replaces the kit's v1 default of 5 retries before
	// dead-lettering (now "archive" in asynq parlance).
	defaultMaxRetries = 5

	// defaultShutdownTimeout caps how long Process waits for in-flight
	// handlers when the parent context is cancelled.
	defaultShutdownTimeout = 30 * time.Second

	// defaultHealthCheckInterval is how often the asynq server pings Redis
	// to surface degraded backends to the kit's logger.
	defaultHealthCheckInterval = 30 * time.Second
)

// Message is a kit queue envelope. The pre-v2 wire format (a JSON struct
// stored on a Redis LIST) is preserved bit-for-bit so handler code that
// unmarshals from `[]byte` is untouched; the envelope now travels inside
// an [asynq.Task] payload rather than a raw LIST entry.
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
	return Message{
		ID:        id.New(),
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
				Help:      "Total messages moved to the asynq archive (dead-letter set).",
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
				Help:      "Number of messages currently active (claimed by a worker) in the asynq queue.",
			},
			[]string{"queue"},
		),
		queueDepth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "queue_depth",
				Help:      "Number of messages waiting in the asynq pending state.",
			},
			[]string{"queue"},
		),
		dlqDepth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "redis",
				Subsystem: "queue",
				Name:      "dlq_depth",
				Help:      "Number of messages currently in the asynq archive (dead-letter). Polled alongside queue_depth so operators can alert on a growing DLQ without standing up a separate poller.",
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
	m.processingDepth = promutil.MustRegisterOrGet(reg, m.processingDepth)
	m.queueDepth = promutil.MustRegisterOrGet(reg, m.queueDepth)
	m.dlqDepth = promutil.MustRegisterOrGet(reg, m.dlqDepth)

	return m
}

var defaultMetrics = sync.OnceValue(func() *Metrics { return NewMetrics() })

func queueMetricLabel(queue string) string {
	return promutil.OpaqueLabelValue("queue", queue)
}

// Queue provides a kit-friendly seam over [hibiken/asynq]. Replaces the
// pre-v2 LIST + heartbeat scheme with asynq's claim model and
// invisibility-timeout (configurable via [WithInvisibilityTimeout]).
//
// The kit's [Queue] interface is preserved; consumers continue to call
// [Queue.Enqueue]/[Queue.EnqueueBatch] and [Queue.Process] with the same
// signatures.
//
// Wire envelope: every enqueue creates an [asynq.Task] of type
// `rho.envelope` whose payload is the kit's [Message] JSON. Asynq's queue
// (the per-tenant queue name) becomes the routing key. Stuck-task recovery
// is governed by asynq's invisibility timeout — a worker that crashes
// mid-handler has its task re-enqueued after the timeout (default 30s)
// instead of the kit's per-task heartbeat scheme.
//
// Concurrency: Enqueue and Len are safe for concurrent use. Process is
// single-call per queue name — calling Process concurrently on the same
// queue will panic; the active-queue guard prevents double-starting an
// asynq server against the same queue.
type Queue struct {
	client    *asynq.Client
	inspector *asynq.Inspector
	logger    *slog.Logger

	consumerID string

	maxRetries        int
	maxPayloadSize    int
	concurrency       int
	invisibilityTO    time.Duration
	shutdownTimeout   time.Duration
	healthCheckPeriod time.Duration
	retentionTTL      time.Duration

	metrics *Metrics

	serverFactory func(cfg asynq.Config) asynqServer

	activeQueuesMu sync.Mutex
	activeQueues   map[string]bool
}

// asynqServer is the subset of [*asynq.Server] the kit uses, kept as an
// interface so tests can swap in a fake server without standing up a full
// Redis stack. The kit deliberately calls Start (non-blocking) +
// Shutdown rather than Run — Run blocks on SIGINT/SIGTERM, which is the
// wrong control plane for a library that owns no main() and must shut
// down on parent-context cancellation.
type asynqServer interface {
	Start(handler asynq.Handler) error
	Shutdown()
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

// WithMaxRetries sets the maximum processing attempts before the task is
// archived (asynq's "dead-letter"). Negative values panic. Zero archives
// on the first handler error.
func WithMaxRetries(n int) Option {
	if n < 0 {
		panic("redisqueue: WithMaxRetries requires n >= 0")
	}
	return func(q *Queue) {
		q.maxRetries = n
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
// metrics. If not set, prometheus.DefaultRegisterer is used. Passing
// nil panics — omit the option to use the default registerer.
//
// Mirrors the panic-on-nil contract of [WithRegisterer] (the metric
// constructor option) so callers cannot accidentally silence
// "registerer was unset" wiring bugs at either layer.
func WithMetricsRegisterer(reg prometheus.Registerer) Option {
	if reg == nil {
		panic("redisqueue: WithMetricsRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(q *Queue) {
		q.metrics = NewMetrics(WithRegisterer(reg))
	}
}

// WithConsumerID overrides the auto-generated consumer ID. The ID is
// emitted in log lines and exposed via [Queue.ConsumerID]; it does NOT
// affect asynq's task-claim semantics (asynq scopes claims by server
// instance, not by a kit-supplied identifier).
//
// Panics on IDs longer than [maxConsumerIDLen] or containing characters
// outside [A-Za-z0-9_-]. Long IDs inflate every log line; ':' and other
// delimiters confuse downstream operators reading logs.
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

// WithConcurrency sets the per-Process asynq worker pool size. Default is
// 1, which preserves the pre-v2 single-consumer-per-queue behaviour.
// Larger values opt in to asynq's parallel dispatch. Must be positive.
func WithConcurrency(n int) Option {
	if n <= 0 {
		panic("redisqueue: WithConcurrency requires n > 0")
	}
	return func(q *Queue) {
		q.concurrency = n
	}
}

// WithInvisibilityTimeout sets how long asynq waits before re-enqueueing
// an in-flight task whose worker has not acknowledged completion. Replaces
// the pre-v2 heartbeat scheme: a worker that crashes mid-handler leaks its
// task only for the invisibility timeout. Default matches asynq's
// upstream default (30s). Must be positive.
func WithInvisibilityTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("redisqueue: WithInvisibilityTimeout requires a positive duration")
	}
	return func(q *Queue) {
		q.invisibilityTO = d
	}
}

// WithShutdownTimeout overrides the asynq server's graceful-shutdown
// timeout (the grace period an in-flight handler is given to return after
// the parent context is cancelled). Default is 30s. Must be positive.
func WithShutdownTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("redisqueue: WithShutdownTimeout requires a positive duration")
	}
	return func(q *Queue) {
		q.shutdownTimeout = d
	}
}

// WithRetention sets the asynq retention period for completed tasks. By
// default, asynq deletes a task as soon as the handler returns nil; the
// retention TTL keeps the asynq Inspector able to display recent
// completions for operator triage. Zero (default) disables retention.
// Must be non-negative.
func WithRetention(d time.Duration) Option {
	if d < 0 {
		panic("redisqueue: WithRetention requires d >= 0")
	}
	return func(q *Queue) {
		q.retentionTTL = d
	}
}

// NewQueue creates a queue backed by asynq, with the given go-redis client
// providing the underlying Redis connection.
//
// Panics if client is nil, options are invalid, or the auto-generated
// consumer ID cannot be generated. A consumer-ID generation failure is
// pathological — it means crypto/rand returned an error, which only happens
// on a broken OS — so panicking matches the kit-wide convention for
// invariants the runtime cannot recover from.
func NewQueue(client goredis.UniversalClient, opts ...Option) *Queue {
	if client == nil {
		panic("redisqueue: NewQueue requires a non-nil Redis client")
	}
	q := &Queue{
		client:            asynq.NewClientFromRedisClient(client),
		inspector:         asynq.NewInspectorFromRedisClient(client),
		logger:            slog.Default(),
		maxRetries:        defaultMaxRetries,
		maxPayloadSize:    defaultMaxPayloadSize,
		concurrency:       defaultConcurrency,
		invisibilityTO:    0,
		shutdownTimeout:   defaultShutdownTimeout,
		healthCheckPeriod: defaultHealthCheckInterval,
		metrics:           defaultMetrics(),
		activeQueues:      make(map[string]bool),
	}
	q.serverFactory = func(cfg asynq.Config) asynqServer {
		return asynq.NewServerFromRedisClient(client, cfg)
	}
	for _, o := range opts {
		if o == nil {
			panic("redisqueue: NewQueue option must not be nil")
		}
		o(q)
	}
	if q.consumerID == "" {
		q.consumerID = newQueueConsumerID()
	}

	return q
}

func (q *Queue) ready() error {
	if q == nil ||
		q.client == nil ||
		q.inspector == nil ||
		q.logger == nil ||
		q.metrics == nil ||
		!validConsumerID(q.consumerID) ||
		q.maxRetries < 0 ||
		q.maxPayloadSize < 0 ||
		q.concurrency <= 0 ||
		q.shutdownTimeout <= 0 ||
		q.activeQueues == nil {
		return kitqueue.ErrInvalidQueue
	}
	return nil
}

// ConsumerID returns the unique identifier for this Queue instance. Stable
// for the lifetime of the Queue; emitted in logs to disambiguate workers.
func (q *Queue) ConsumerID() string {
	if q == nil {
		return ""
	}
	return q.consumerID
}

// Close releases the underlying asynq client + inspector. The Redis client
// supplied to [NewQueue] is NOT closed (asynq's docs require the caller to
// own its lifecycle). Returns the first non-nil close error.
func (q *Queue) Close() error {
	if q == nil {
		return nil
	}
	var firstErr error
	if q.client != nil {
		if err := q.client.Close(); err != nil {
			firstErr = fmt.Errorf("close asynq client: %w", err)
		}
	}
	if q.inspector != nil {
		if err := q.inspector.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close asynq inspector: %w", err)
		}
	}
	return firstErr
}

// Enqueue adds a message to the named queue. Returns an error if the
// serialized message exceeds the configured max payload size or if
// Message.ID is not a safe identifier (see [validateMessageID]).
// Caller-supplied IDs are used as asynq's per-queue unique TaskID, which
// gives FR-059 idempotency by default — a second Enqueue with the same
// (queue, id) returns [asynq.ErrTaskIDConflict] wrapped in a non-nil error.
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
	// Belt-and-braces cap on the full envelope (validateMessage already
	// bounded the inner Payload). Use the same headroom the handler side
	// applies so a max-sized payload is never rejected after marshal
	// purely because the JSON envelope adds structural bytes.
	if envelopeLimit := q.envelopeLimit(); envelopeLimit > 0 && len(data) > envelopeLimit {
		return &kitqueue.MessageTooLargeError{Size: len(data), Limit: envelopeLimit}
	}
	task := asynq.NewTask(envelopeTaskType, data)
	if _, err := q.client.EnqueueContext(ctx, task, q.enqueueOpts(queue, msg.ID)...); err != nil {
		return fmt.Errorf("asynq enqueue: %w", err)
	}
	q.metrics.messagesEnqueued.WithLabelValues(queueMetricLabel(queue)).Inc()
	return nil
}

// EnqueueBatch adds multiple messages by issuing one asynq.Enqueue per
// message. Asynq has no batch enqueue primitive — calls are sequential to
// keep partial-failure semantics predictable. On the first error the
// already-enqueued messages remain in the queue and the call returns the
// error wrapping the index of the failing message.
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

	label := queueMetricLabel(queue)
	envelopeLimit := q.envelopeLimit()
	for i, msg := range msgs {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal message [%d]: %w", i, err)
		}
		// Envelope cap matches the handler side (maxPayloadSize +
		// queueEnvelopeOverhead) so a max-sized payload that survived
		// validateMessage cannot fail this belt-and-braces check.
		if envelopeLimit > 0 && len(data) > envelopeLimit {
			return fmt.Errorf("message [%d]: %w", i, &kitqueue.MessageTooLargeError{Size: len(data), Limit: envelopeLimit})
		}
		task := asynq.NewTask(envelopeTaskType, data)
		if _, err := q.client.EnqueueContext(ctx, task, q.enqueueOpts(queue, msg.ID)...); err != nil {
			return fmt.Errorf("asynq enqueue [%d]: %w", i, err)
		}
		q.metrics.messagesEnqueued.WithLabelValues(label).Inc()
	}
	return nil
}

// envelopeLimit returns the maximum allowed marshaled-envelope size in
// bytes (Payload + JSON envelope headers). Mirrors the receive-side cap
// in handlerForQueue so send/receive agree on the boundary. Returns 0
// when [Queue.maxPayloadSize] is 0 (cap disabled).
func (q *Queue) envelopeLimit() int {
	if q == nil || q.maxPayloadSize <= 0 {
		return 0
	}
	return q.maxPayloadSize + queueEnvelopeOverhead
}

// enqueueOpts builds the per-message asynq option set. The TaskID is set
// to the caller-supplied Message.ID so duplicate enqueues of the same ID
// to the same queue collapse via asynq's idempotency (FR-059); the
// per-message Timeout enforces the kit's invisibility-timeout contract
// (asynq has no per-server knob for this); the per-queue Retention keeps
// completed tasks visible in the asynq UI when [WithRetention] is
// configured.
func (q *Queue) enqueueOpts(queue, msgID string) []asynq.Option {
	opts := []asynq.Option{
		asynq.Queue(queue),
		asynq.TaskID(msgID),
		asynq.MaxRetry(q.maxRetries),
	}
	if q.invisibilityTO > 0 {
		opts = append(opts, asynq.Timeout(q.invisibilityTO))
	}
	if q.retentionTTL > 0 {
		opts = append(opts, asynq.Retention(q.retentionTTL))
	}
	return opts
}

// Process subscribes to the named queue and dispatches each message to
// handler. Blocks until ctx is cancelled. Internally starts an
// [asynq.Server] scoped to this single queue; multiple Process calls (on
// the same or different queue names) each run independent servers.
//
// Important: handlers must be idempotent. Asynq re-enqueues stuck tasks
// after the invisibility timeout; a worker that crashed mid-handler may
// see the same task delivered again to another worker.
//
// Panics if queue name is empty, handler is nil, or another Process call
// is already active for the same queue name on this Queue instance — fail
// fast at startup rather than on the first message.
func (q *Queue) Process(ctx context.Context, queue string, handler Handler) {
	if err := q.ready(); err != nil {
		panic("redisqueue: Process requires an initialised queue")
	}
	if err := redis.ValidateName(queue, "queue"); err != nil {
		panic("redisqueue: Process requires a valid queue name")
	}
	if handler == nil {
		panic("redisqueue: Process requires a non-nil handler")
	}
	if q.serverFactory == nil {
		panic("redisqueue: Process requires an initialised server factory")
	}

	q.activeQueuesMu.Lock()
	if q.activeQueues[queue] {
		q.activeQueuesMu.Unlock()
		panic("redisqueue: Process queue already has an active Process goroutine")
	}
	q.activeQueues[queue] = true
	q.activeQueuesMu.Unlock()
	defer func() {
		q.activeQueuesMu.Lock()
		delete(q.activeQueues, queue)
		q.activeQueuesMu.Unlock()
	}()

	cfg := q.buildServerConfig(queue, handler)
	srv := q.serverFactory(cfg)

	depthStop := q.startDepthPoller(ctx, queue)
	defer depthStop()

	asynqHandler := q.handlerForQueue(queue, handler)

	// Start kicks off the asynq worker pool synchronously and returns
	// once the pool is ready. We then block on the parent context so the
	// kit's lifecycle (StartProcessors + waitgroup) keeps working
	// unchanged: Process returns when ctx is cancelled.
	if err := srv.Start(asynqHandler); err != nil {
		q.logger.Error("asynq server failed to start",
			redact.String("queue", queue),
			redact.String("consumer_id", q.consumerID),
			redact.Error(err),
		)
		return
	}
	<-ctx.Done()
	// Shutdown drains in-flight handlers up to ShutdownTimeout. Per
	// asynq's contract Shutdown is called once and is the only teardown
	// signal the kit needs (Server.Stop just toggles internal state and
	// is unused by the wrapper).
	srv.Shutdown()
}

// buildServerConfig translates kit options into [asynq.Config]. The
// per-queue priority map ties this server to exactly one queue name so
// concurrent Process calls on the same Queue instance do not race on
// asynq's internal scheduling.
//
// Asynq's "invisibility timeout" is enforced per-task via the
// [asynq.Timeout] enqueue option (see [enqueueOpts]) rather than via a
// per-server knob — this method only translates server-scoped concerns.
func (q *Queue) buildServerConfig(queue string, _ Handler) asynq.Config {
	return asynq.Config{
		Concurrency: q.concurrency,
		Queues: map[string]int{
			queue: 1,
		},
		ShutdownTimeout:     q.shutdownTimeout,
		HealthCheckFunc:     q.healthCheckFunc(queue),
		HealthCheckInterval: q.healthCheckPeriod,
		Logger:              asynqSlogAdapter{logger: q.logger, consumerID: q.consumerID},
		IsFailure:           func(err error) bool { return err != nil },
		BaseContext:         func() context.Context { return context.Background() },
		ErrorHandler:        asynq.ErrorHandlerFunc(q.onTaskError(queue)),
	}
}

// handlerForQueue returns the asynq.Handler that decodes the kit envelope
// and dispatches to the kit handler.
func (q *Queue) handlerForQueue(queue string, handler Handler) asynq.Handler {
	label := queueMetricLabel(queue)
	return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		data := task.Payload()
		if q.maxPayloadSize > 0 && len(data) > q.maxPayloadSize+queueEnvelopeOverhead {
			err := fmt.Errorf("payload+envelope exceeds %d bytes", q.maxPayloadSize+queueEnvelopeOverhead)
			q.logger.Error("discarding oversize queue message",
				redact.String("queue", queue),
				redact.Error(err),
			)
			q.metrics.messagesFailed.WithLabelValues(label).Inc()
			return redact.WrapSentinel(asynq.SkipRetry, err)
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			q.logger.Error("discarding undecodable queue message",
				redact.String("queue", queue),
				redact.Error(err),
			)
			q.metrics.messagesFailed.WithLabelValues(label).Inc()
			return redact.WrapSentinel(
				fmt.Errorf("%w: unmarshal envelope", asynq.SkipRetry), err,
			)
		}
		if err := validateMessage(msg, q.maxPayloadSize); err != nil {
			q.logger.Error("discarding invalid queue message",
				redact.String("queue", queue),
				redact.Error(err),
			)
			q.metrics.messagesFailed.WithLabelValues(label).Inc()
			return redact.WrapSentinel(asynq.SkipRetry, err)
		}

		retryCount, _ := asynq.GetRetryCount(ctx)
		// asynq's retry count is zero-based for the first attempt. The kit's
		// pre-v2 Message.Attempt was one-based — preserve that for handler
		// compatibility.
		msg.Attempt = retryCount + 1

		start := time.Now()
		err := callHandler(ctx, handler, msg)
		q.metrics.processingDuration.WithLabelValues(label).Observe(time.Since(start).Seconds())

		if err != nil {
			q.metrics.messagesFailed.WithLabelValues(label).Inc()
			if apperror.IsPermanent(err) {
				// Permanent errors skip retries and route straight to the archive.
				return redact.WrapSentinel(asynq.SkipRetry, err)
			}
			q.metrics.messagesRetried.WithLabelValues(label).Inc()
			return err
		}
		q.metrics.messagesProcessed.WithLabelValues(label).Inc()
		return nil
	})
}

// onTaskError is the per-server ErrorHandler. Asynq invokes it AFTER the
// handler returns an error and AFTER the retry decision is made. We use it
// to count dead-lettered (archived) tasks and surface log lines with the
// kit's redact discipline.
func (q *Queue) onTaskError(queue string) func(ctx context.Context, t *asynq.Task, err error) {
	label := queueMetricLabel(queue)
	return func(ctx context.Context, t *asynq.Task, err error) {
		retried, _ := asynq.GetRetryCount(ctx)
		maxRetry, _ := asynq.GetMaxRetry(ctx)
		taskID, _ := asynq.GetTaskID(ctx)
		if retried >= maxRetry || errors.Is(err, asynq.SkipRetry) {
			q.metrics.messagesDeadLettered.WithLabelValues(label).Inc()
			q.logger.Error("queue message archived after exhausting retries",
				redact.String("queue", queue),
				redact.String("task_id", taskID),
				"retried", retried,
				"max_retry", maxRetry,
				redact.Error(err),
			)
			return
		}
		q.logger.Warn("queue handler returned error; asynq will retry",
			redact.String("queue", queue),
			redact.String("task_id", taskID),
			"retried", retried,
			"max_retry", maxRetry,
			redact.Error(err),
		)
	}
}

// healthCheckFunc is invoked periodically by asynq with the most recent
// broker-ping error (nil when healthy). We log degraded states so
// operators see Redis connectivity problems without standing up extra
// metrics.
func (q *Queue) healthCheckFunc(queue string) func(error) {
	return func(err error) {
		if err == nil {
			return
		}
		q.logger.Warn("asynq broker health check failed",
			redact.String("queue", queue),
			redact.String("consumer_id", q.consumerID),
			redact.Error(err),
		)
	}
}

// Len returns the number of pending (waiting) messages in the named queue
// using asynq's Inspector. Reports zero (no error) for an unknown queue,
// matching pre-v2 LLEN-of-missing-key behaviour.
func (q *Queue) Len(ctx context.Context, queue string) (int64, error) {
	if err := q.ready(); err != nil {
		return 0, err
	}
	if err := redis.ValidateName(queue, "queue"); err != nil {
		return 0, err
	}
	// asynq.Inspector.GetQueueInfo doesn't accept a context; wrap a
	// best-effort cancellation check so callers passing a cancelled ctx
	// don't pay for a Redis round-trip.
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	info, err := q.inspector.GetQueueInfo(queue)
	if err != nil {
		if isQueueNotFoundError(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("inspect queue: %w", err)
	}
	return int64(info.Pending), nil
}

// isQueueNotFoundError detects asynq's "queue does not exist" condition.
// asynq's Inspector.GetQueueInfo does NOT wrap the exported
// [asynq.ErrQueueNotFound] sentinel — it returns its internal `errors.E`
// type whose string starts with `NOT_FOUND:`. We match on that prefix
// because there is no public Go API to type-assert it.
func isQueueNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, asynq.ErrQueueNotFound) {
		return true
	}
	return strings.HasPrefix(err.Error(), "NOT_FOUND:") ||
		strings.Contains(err.Error(), "NOT_FOUND: queue ")
}

// asynqSlogAdapter routes asynq's [asynq.Logger] interface into the kit's
// [slog.Logger] so operators see queue server lifecycle events alongside
// the rest of the kit's structured logs.
type asynqSlogAdapter struct {
	logger     *slog.Logger
	consumerID string
}

func (a asynqSlogAdapter) log(level slog.Level, args ...any) {
	if a.logger == nil {
		return
	}
	msg := fmt.Sprint(args...)
	a.logger.Log(context.Background(), level, msg, redact.String("consumer_id", a.consumerID))
}

func (a asynqSlogAdapter) Debug(args ...any) { a.log(slog.LevelDebug, args...) }
func (a asynqSlogAdapter) Info(args ...any)  { a.log(slog.LevelInfo, args...) }
func (a asynqSlogAdapter) Warn(args ...any)  { a.log(slog.LevelWarn, args...) }
func (a asynqSlogAdapter) Error(args ...any) { a.log(slog.LevelError, args...) }
func (a asynqSlogAdapter) Fatal(args ...any) { a.log(slog.LevelError, args...) }

// _ asserts the slog adapter implements asynq.Logger.
var _ asynq.Logger = asynqSlogAdapter{}
