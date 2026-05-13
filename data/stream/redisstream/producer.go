package redisstream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/redact"
	kitstream "github.com/bds421/rho-kit/data/v2/stream"
	"github.com/bds421/rho-kit/infra/redis/v2"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// defaultStreamMaxPayloadSize is the default max payload size for stream messages (1 MiB).
const defaultStreamMaxPayloadSize = 1 << 20 // 1 MiB

// ProducerMetrics holds Prometheus collectors for stream producer monitoring.
type ProducerMetrics struct {
	messagesProduced *prometheus.CounterVec
}

// NewProducerMetrics creates and registers producer metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewProducerMetrics(reg prometheus.Registerer) *ProducerMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &ProducerMetrics{
		messagesProduced: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "stream",
				Name:      "messages_produced_total",
				Help:      "Total messages produced to streams.",
			},
			[]string{"stream"},
		),
	}

	m.messagesProduced = promutil.MustRegisterOrGet(reg, m.messagesProduced)

	return m
}

var defaultProducerMetrics = sync.OnceValue(func() *ProducerMetrics { return NewProducerMetrics(nil) })

func streamMetricLabel(stream string) string {
	return promutil.OpaqueLabelValue("stream", stream)
}

func groupMetricLabel(group string) string {
	return promutil.OpaqueLabelValue("group", group)
}

// Producer publishes messages to Redis streams with delivery guarantees.
type Producer struct {
	client goredis.UniversalClient
	logger *slog.Logger

	// maxLen controls stream trimming. 0 means no trimming.
	// Uses approximate trimming (~maxLen) for performance.
	maxLen int64

	// retention controls time-based stream trimming via MINID.
	// Entries older than this duration are trimmed approximately.
	// 0 means no time-based trimming.
	// Mutually exclusive with maxLen — if both are set, maxLen takes precedence.
	retention time.Duration

	// maxPayloadSize is the maximum payload size in bytes. 0 means no limit.
	maxPayloadSize int

	// unbounded opts out of the FR-062 default retention (7 days).
	unbounded bool

	metrics *ProducerMetrics
}

// ProducerOption configures a Producer.
type ProducerOption func(*Producer)

// WithProducerLogger sets the logger for the producer. Nil is
// normalised to [slog.Default] (audit FR-063).
func WithProducerLogger(l *slog.Logger) ProducerOption {
	if l == nil {
		l = slog.Default()
	}
	return func(p *Producer) { p.logger = l }
}

// WithMaxStreamLen sets the approximate maximum stream length. Messages
// beyond this limit are trimmed using Redis MAXLEN ~N (approximate trimming
// for better performance). 0 disables length-based trimming. Negative values
// panic.
//
// Mutually exclusive with WithRetention — if both are set, MaxLen takes precedence.
func WithMaxStreamLen(n int64) ProducerOption {
	if n < 0 {
		panic("redisstream: WithMaxStreamLen requires n >= 0")
	}
	return func(p *Producer) {
		p.maxLen = n
	}
}

// WithProducerMaxPayloadSize sets the maximum payload size in bytes.
// Messages with a payload exceeding this limit are rejected at publish time.
// Default is 1 MiB. Set to 0 to disable the limit entirely (use with caution).
// Negative values panic.
func WithProducerMaxPayloadSize(n int) ProducerOption {
	if n < 0 {
		panic("redisstream: WithProducerMaxPayloadSize requires n >= 0")
	}
	return func(p *Producer) {
		p.maxPayloadSize = n
	}
}

// WithRetention enables time-based stream trimming via Redis MINID ~<id>.
// Entries older than the given duration are approximately trimmed on each
// publish. For example, WithRetention(7 * 24 * time.Hour) keeps roughly
// one week of entries. The duration must be positive; use
// [WithUnboundedStream] to opt out of default retention.
//
// Mutually exclusive with WithMaxStreamLen — if both are set, MaxLen takes precedence.
func WithRetention(d time.Duration) ProducerOption {
	if d <= 0 {
		panic("redisstream: WithRetention requires a positive duration")
	}
	return func(p *Producer) {
		p.retention = d
	}
}

// WithProducerRegisterer sets the Prometheus registerer for producer metrics.
// If not set, prometheus.DefaultRegisterer is used.
func WithProducerRegisterer(reg prometheus.Registerer) ProducerOption {
	return func(p *Producer) {
		p.metrics = NewProducerMetrics(reg)
	}
}

// NewProducer creates a producer that writes to Redis streams.
// The default maximum payload size is 1 MiB. Override with
// WithProducerMaxPayloadSize. Set to 0 to disable the limit. Panics if
// client is nil — a miswired producer would otherwise dereference nil on
// the first Publish.
//
// Audit FR-062: a producer constructed without [WithMaxStreamLen]
// AND without [WithRetention] would let the stream grow forever.
// Pre-fix this was easy to miss — both options were "if non-zero,
// apply" with no default. The constructor now installs
// [defaultStreamRetention] (7 days) when neither is configured;
// callers that genuinely want unbounded streams must opt in via
// [WithUnboundedStream].
func NewProducer(client goredis.UniversalClient, opts ...ProducerOption) *Producer {
	if client == nil {
		panic("redisstream: NewProducer requires a non-nil Redis client")
	}
	p := &Producer{
		client:         client,
		logger:         slog.Default(),
		maxPayloadSize: defaultStreamMaxPayloadSize,
		metrics:        defaultProducerMetrics(),
	}
	for _, o := range opts {
		if o == nil {
			panic("redisstream: NewProducer option must not be nil")
		}
		o(p)
	}
	// FR-063 [MED]: WithProducerLogger silently ignored a nil
	// argument. Normalise here so a caller passing nil (or omitting
	// the option entirely) still gets a usable logger.
	if p.logger == nil {
		p.logger = slog.Default()
	}
	if p.maxLen == 0 && p.retention == 0 && !p.unbounded {
		p.retention = defaultStreamRetention
	}
	return p
}

func (p *Producer) ready() error {
	if p == nil ||
		p.client == nil ||
		p.logger == nil ||
		p.maxLen < 0 ||
		p.retention < 0 ||
		p.maxPayloadSize < 0 ||
		(p.maxLen == 0 && p.retention == 0 && !p.unbounded) ||
		p.metrics == nil {
		return kitstream.ErrInvalidStream
	}
	return nil
}

// defaultStreamRetention bounds Redis-stream growth when no
// retention option is set (audit FR-062). 7 days is generous for
// most event streams and prevents unbounded retention by default.
const defaultStreamRetention = 7 * 24 * time.Hour

// WithUnboundedStream opts a producer out of the default retention
// (audit FR-062). Use only for genuinely append-only streams whose
// growth is bounded by an external lifecycle (e.g. test harnesses).
func WithUnboundedStream() ProducerOption {
	return func(p *Producer) { p.unbounded = true }
}

// Publish writes a message to the given stream. The Redis stream ID is
// auto-generated by the server (using *). Returns the server-assigned ID.
//
// The message is stored as a flat hash:
//
//	id       → UUID v7
//	type     → event type
//	payload  → JSON bytes
//	ts       → RFC3339Nano timestamp
//	headers  → JSON-encoded headers map
func (p *Producer) Publish(ctx context.Context, stream string, msg Message) (string, error) {
	if err := p.ready(); err != nil {
		return "", err
	}
	if err := redis.ValidateName(stream, "stream"); err != nil {
		return "", err
	}
	args, err := p.buildXAddArgs(stream, msg)
	if err != nil {
		return "", err
	}

	result, err := p.client.XAdd(ctx, args).Result()
	if err != nil {
		return "", fmt.Errorf("xadd: %w", err)
	}

	p.metrics.messagesProduced.WithLabelValues(streamMetricLabel(stream)).Inc()

	p.logger.Debug("message published to stream",
		redact.String("stream", stream),
		redact.String("redis_id", result),
		redact.String("msg_id", msg.ID),
		redact.String("type", msg.Type),
	)

	return result, nil
}

// PublishBatch writes multiple messages to a stream in a single pipeline.
// Returns the server-assigned IDs in the same order as the input messages.
//
// Note: Redis pipelines are not atomic. If the pipeline partially fails,
// some messages may have been written. Callers should treat a pipeline
// error as "unknown state" and use idempotent message IDs for deduplication.
func (p *Producer) PublishBatch(ctx context.Context, stream string, msgs []Message) ([]string, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if err := redis.ValidateName(stream, "stream"); err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	if len(msgs) > MaxBatchMessages {
		return nil, ErrBatchTooLarge
	}

	// Pre-validate all messages before building the pipeline to avoid
	// partially constructing pipeline commands that are then discarded.
	for i, msg := range msgs {
		if err := ValidateMessage(msg, p.maxPayloadSize); err != nil {
			return nil, fmt.Errorf("message [%d]: %w", i, err)
		}
	}

	pipe := p.client.Pipeline()
	cmds := make([]*goredis.StringCmd, len(msgs))

	for i, msg := range msgs {
		args, err := p.buildXAddArgs(stream, msg)
		if err != nil {
			return nil, fmt.Errorf("message [%d]: %w", i, err)
		}
		cmds[i] = pipe.XAdd(ctx, args)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	ids := make([]string, len(cmds))
	var succeeded int
	for i, cmd := range cmds {
		id, err := cmd.Result()
		if err != nil {
			// Record the messages that did succeed before the failure.
			if succeeded > 0 {
				p.metrics.messagesProduced.WithLabelValues(streamMetricLabel(stream)).Add(float64(succeeded))
			}
			return nil, fmt.Errorf("xadd result [%d]: %w", i, err)
		}
		ids[i] = id
		succeeded++
	}

	p.metrics.messagesProduced.WithLabelValues(streamMetricLabel(stream)).Add(float64(succeeded))

	return ids, nil
}

// buildXAddArgs prepares a message for XADD, filling in defaults for
// missing ID and timestamp, and marshalling headers.
func (p *Producer) buildXAddArgs(stream string, msg Message) (*goredis.XAddArgs, error) {
	if msg.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("generate message ID: %w", err)
		}
		msg.ID = id.String()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	if err := ValidateMessage(msg, p.maxPayloadSize); err != nil {
		return nil, err
	}

	values := map[string]any{
		"id":      msg.ID,
		"type":    msg.Type,
		"payload": string(msg.Payload),
		"ts":      msg.Timestamp.Format(time.RFC3339Nano),
	}

	if len(msg.Headers) > 0 {
		headerBytes, err := json.Marshal(msg.Headers)
		if err != nil {
			return nil, fmt.Errorf("marshal headers: %w", err)
		}
		values["headers"] = string(headerBytes)
	}

	args := &goredis.XAddArgs{
		Stream: stream,
		ID:     "*", // Explicitly request server-generated ID.
		Values: values,
	}

	switch {
	case p.maxLen > 0:
		args.MaxLen = p.maxLen
		args.Approx = true
	case p.retention > 0:
		// Redis stream IDs are "<milliseconds>-<seq>". Using "<ms>-0" as
		// MinID trims all entries with a timestamp before the cutoff.
		cutoff := time.Now().Add(-p.retention).UnixMilli()
		args.MinID = fmt.Sprintf("%d-0", cutoff)
		args.Approx = true
	}

	return args, nil
}
