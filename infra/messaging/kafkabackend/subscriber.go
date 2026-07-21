package kafkabackend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// Subscriber consumes messages from one or more Kafka topics via a
// shared consumer group. It implements [messaging.Consumer]: every
// call to Consume binds the underlying kafka-go [kafka.Reader] to the
// topic identified by Binding.Exchange.
//
// Concurrency: one Subscriber is intended for a single Consume
// invocation per Binding. Sharing one Subscriber across overlapping
// Consume calls on different bindings is supported but each call
// constructs a private Reader for the duration of the call (so each
// binding has its own offset state inside the same group).
//
// # Shutdown semantics — at-least-once redelivery
//
// When the parent ctx is cancelled, [Subscriber.dispatch] still calls
// [Reader.CommitMessages] for completed handlers through
// commitWithOutcome. Because the same ctx is the one that triggered
// shutdown, kafka-go's commit may surface a "ctx cancelled" error and
// the offset will not advance on the broker. The next consumer to
// join the group will re-fetch the same message — this is the
// intended at-least-once shape. kafkabackend prefers a duplicate
// delivery on restart over silently advancing an offset on a
// cancelled commit. Operators relying on exactly-once semantics must
// layer idempotency at the handler or downstream-store level; the
// kit's [data/v2/idempotency] package is the canonical hook.
//
// # Handler execution bound
//
// Every handler invocation is capped at [handlerTimeout] (30s), matching
// amqpbackend and natsbackend. A stuck handler is cancelled rather than
// stalling the partition forever. There is no separate shutdown grace:
// on parent ctx cancel the handler still receives the remaining timeout
// window via context.WithoutCancel + WithTimeout (at-least-once redelivery
// still applies if the handler fails after cancel).
//
// # Transient-error reader reset cost
//
// Non-permanent handler errors close and recreate the group Reader so the
// fetch rewinds to the last committed offset. That leave/rejoin triggers a
// consumer-group rebalance. Back-off escalates with resetStreak (500ms base,
// doubling to 30s) to limit rebalance storms under persistent failures.
const handlerTimeout = 30 * time.Second

type Subscriber struct {
	cfg     Config
	groupID string
	topics  []string
	options subscriberOptions
	logger  *slog.Logger
	metrics *Metrics

	// resetStreak counts consecutive reader resets due to transient
	// handler errors; used only to escalate the reset back-off.
	resetStreak int
}

// SubscriberOption configures a [Subscriber].
type SubscriberOption func(*subscriberOptions)

type subscriberOptions struct {
	minBytes          int
	maxBytes          int
	maxWait           time.Duration
	startOffset       int64
	heartbeatInterval time.Duration
	sessionTimeout    time.Duration
	rebalanceTimeout  time.Duration
	commitInterval    time.Duration
	queueCapacity     int
	logger            *slog.Logger
	metrics           *Metrics
	dialer            *kafka.Dialer
}

// WithMinBytes overrides kafka.ReaderConfig.MinBytes (default kafka-go
// default = 1 byte). Larger values reduce broker load on low-volume
// topics at the cost of latency.
func WithMinBytes(n int) SubscriberOption {
	if n < 0 {
		panic("kafkabackend: WithMinBytes requires n >= 0")
	}
	return func(o *subscriberOptions) { o.minBytes = n }
}

// WithMaxBytes overrides kafka.ReaderConfig.MaxBytes (default 1 MiB).
// This bounds the size of a single fetch response, NOT the maximum
// per-message size — set the topic's max.message.bytes and the
// publisher's WithMaxMessageBytes to cap individual records.
func WithMaxBytes(n int) SubscriberOption {
	if n <= 0 {
		panic("kafkabackend: WithMaxBytes requires n > 0")
	}
	return func(o *subscriberOptions) { o.maxBytes = n }
}

// WithMaxWait overrides kafka.ReaderConfig.MaxWait (default 10s) —
// the longest the broker will wait to satisfy MinBytes before
// returning an incomplete batch.
func WithMaxWait(d time.Duration) SubscriberOption {
	if d <= 0 {
		panic("kafkabackend: WithMaxWait requires a positive duration")
	}
	return func(o *subscriberOptions) { o.maxWait = d }
}

// WithStartOffset overrides where a new consumer group starts when no
// committed offset is found for a partition. Default:
// [kafka.FirstOffset] (replay everything). Pass [kafka.LastOffset] to
// skip the backlog and only consume new records.
//
// Note: this only applies to NEW groups. An existing group's
// committed offsets are honoured regardless of this setting.
func WithStartOffset(off int64) SubscriberOption {
	switch off {
	case kafka.FirstOffset, kafka.LastOffset:
	default:
		panic("kafkabackend: WithStartOffset must be kafka.FirstOffset or kafka.LastOffset")
	}
	return func(o *subscriberOptions) { o.startOffset = off }
}

// WithHeartbeatInterval overrides kafka.ReaderConfig.HeartbeatInterval
// (default 3s). Should remain well below SessionTimeout.
func WithHeartbeatInterval(d time.Duration) SubscriberOption {
	if d <= 0 {
		panic("kafkabackend: WithHeartbeatInterval requires a positive duration")
	}
	return func(o *subscriberOptions) { o.heartbeatInterval = d }
}

// WithSessionTimeout overrides kafka.ReaderConfig.SessionTimeout
// (default 30s). The coordinator considers a member dead and starts a
// rebalance after this many seconds without a heartbeat.
func WithSessionTimeout(d time.Duration) SubscriberOption {
	if d <= 0 {
		panic("kafkabackend: WithSessionTimeout requires a positive duration")
	}
	return func(o *subscriberOptions) { o.sessionTimeout = d }
}

// WithRebalanceTimeout overrides kafka.ReaderConfig.RebalanceTimeout
// (default 30s).
func WithRebalanceTimeout(d time.Duration) SubscriberOption {
	if d <= 0 {
		panic("kafkabackend: WithRebalanceTimeout requires a positive duration")
	}
	return func(o *subscriberOptions) { o.rebalanceTimeout = d }
}

// WithCommitInterval enables asynchronous commits at the given
// interval (default 0 = synchronous commits on every successful
// handler return). Asynchronous commits trade durability for
// throughput — on a crash, recently-handled messages may be
// re-delivered.
func WithCommitInterval(d time.Duration) SubscriberOption {
	if d < 0 {
		panic("kafkabackend: WithCommitInterval requires a non-negative duration")
	}
	return func(o *subscriberOptions) { o.commitInterval = d }
}

// WithQueueCapacity overrides kafka.ReaderConfig.QueueCapacity
// (default 100). The Reader's internal pre-fetch buffer.
func WithQueueCapacity(n int) SubscriberOption {
	if n <= 0 {
		panic("kafkabackend: WithQueueCapacity requires n > 0")
	}
	return func(o *subscriberOptions) { o.queueCapacity = n }
}

// WithSubscriberLogger overrides the subscriber logger.
func WithSubscriberLogger(l *slog.Logger) SubscriberOption {
	return func(o *subscriberOptions) { o.logger = l }
}

// WithSubscriberMetrics attaches Prometheus metrics to the
// subscriber.
func WithSubscriberMetrics(m *Metrics) SubscriberOption {
	if m == nil {
		panic("kafkabackend: WithSubscriberMetrics requires non-nil metrics")
	}
	return func(o *subscriberOptions) { o.metrics = m }
}

// NewSubscriber is a convenience wrapper around [NewSubscriberWithConfig]
// with only brokers set. Like [NewPublisher], FR-073 validation requires
// TLS, SASL, or AllowInsecure on Config — so this form cannot succeed in
// production. Prefer [NewSubscriberWithConfig].
//
// groupID must be non-empty; the kit refuses to fabricate a stable
// group ID for the caller.
func NewSubscriber(brokers []string, groupID string, topics []string, opts ...SubscriberOption) (*Subscriber, error) {
	cfg := Config{Brokers: brokers}
	return NewSubscriberWithConfig(cfg, groupID, topics, opts...)
}

// NewSubscriberWithConfig is the full-fidelity constructor used when
// callers need TLS or SASL on the wire.
func NewSubscriberWithConfig(cfg Config, groupID string, topics []string, opts ...SubscriberOption) (*Subscriber, error) {
	cfg, err := cfg.Clone()
	if err != nil {
		return nil, err
	}
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if groupID == "" {
		return nil, errors.New("kafkabackend: NewSubscriber requires a non-empty groupID")
	}
	if len(topics) == 0 {
		return nil, errors.New("kafkabackend: NewSubscriber requires at least one topic")
	}
	for i, topic := range topics {
		if topic == "" {
			return nil, fmt.Errorf("kafkabackend: topics[%d] must not be empty", i)
		}
	}
	options := subscriberOptions{
		startOffset: kafka.FirstOffset,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("kafkabackend: NewSubscriber option must not be nil")
		}
		opt(&options)
	}
	if options.logger == nil {
		options.logger = slog.Default()
	}
	if options.dialer == nil {
		dialer, err := buildDialer(cfg)
		if err != nil {
			return nil, err
		}
		options.dialer = dialer
	}
	return &Subscriber{
		cfg:     cfg,
		groupID: groupID,
		topics:  append([]string(nil), topics...),
		options: options,
		logger:  options.logger,
		metrics: options.metrics,
	}, nil
}

// Group reports the consumer-group ID this subscriber was constructed
// with. Used by [Consume] to validate Binding.ConsumerGroup.
func (s *Subscriber) Group() string {
	if s == nil {
		return ""
	}
	return s.groupID
}

// Topics reports the topic set this subscriber was constructed with.
func (s *Subscriber) Topics() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.topics...)
}

func (s *Subscriber) ready() error {
	if s == nil || s.logger == nil || s.groupID == "" || len(s.topics) == 0 {
		return messaging.ErrInvalidConsumer
	}
	return nil
}

// Consume satisfies [messaging.Consumer]. The Binding.Exchange must
// name a topic this subscriber was constructed with; Binding.ConsumerGroup,
// when non-empty, must equal [Group]. Returns nil on graceful
// shutdown (ctx cancelled) or an error if Reader construction fails.
//
// Retry behaviour: when a handler returns a non-nil error, the
// committed offset is NOT advanced and the Reader is reset so it
// rewinds to the last committed offset; the failed record is then
// re-fetched and redelivered (a later success on the same partition
// can no longer commit a watermark past it). The same record is also
// re-delivered after a group rebalance or restart. A permanent error
// ([apperror.IsPermanent]) is treated as a poison pill: the offset IS
// committed so the consumer can make forward progress.
//
// Binding.Retry is REJECTED at Consume entry — wave 141 turned the
// previous silent log-warning into a hard refusal via
// [messaging.ErrRetryUnsupported]. Kafka has no per-message TTL or
// delayed-redelivery primitive that maps to the kit's RetryPolicy.
// Callers must set [messaging.BindingSpec.WithoutRetry]=true (ack-and-
// discard semantics) or wrap the handler in the kit's
// [resilience/retry] package.
func (s *Subscriber) Consume(ctx context.Context, b messaging.Binding, handler messaging.Handler) error {
	if err := s.ready(); err != nil {
		return err
	}
	if handler == nil {
		return messaging.ErrInvalidConsumer
	}
	if err := messaging.ValidateExchangeName(b.Exchange); err != nil {
		return err
	}
	if b.ConsumerGroup != "" && b.ConsumerGroup != s.groupID {
		return fmt.Errorf("kafkabackend: Binding.ConsumerGroup %q does not match subscriber group %q — construct a separate Subscriber per group", b.ConsumerGroup, s.groupID)
	}
	if !s.subscribesTo(b.Exchange) {
		return fmt.Errorf("kafkabackend: topic %q is not in the subscriber topic set", b.Exchange)
	}
	if b.Retry != nil {
		// Kafka has no per-message redelivery primitive analogous to
		// AMQP DLX. Fail fast so the operator must explicitly opt
		// in to ack-and-discard (WithoutRetry=true) or implement
		// retry in the handler.
		return messaging.ErrRetryUnsupported
	}
	reader, err := s.newReader(b.Exchange)
	if err != nil {
		return err
	}
	defer func() {
		if reader == nil {
			return
		}
		if closeErr := reader.Close(); closeErr != nil {
			s.logger.Warn("kafkabackend: reader close failed",
				redact.String("topic", b.Exchange),
				redact.Error(closeErr),
			)
		}
	}()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		km, err := reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			if isFatalKafkaFetchError(err) {
				s.logger.Error("kafkabackend: fatal fetch error — stopping consumer",
					redact.String("topic", b.Exchange),
					redact.Error(err),
				)
				s.metrics.observeConsumed(b.Exchange, s.groupID, kafkaConsumeOutcomeFetchError)
				return redact.WrapError("kafkabackend: fatal fetch error", err)
			}
			s.logger.Warn("kafkabackend: fetch failed",
				redact.String("topic", b.Exchange),
				redact.Error(err),
			)
			s.metrics.observeConsumed(b.Exchange, s.groupID, kafkaConsumeOutcomeFetchError)
			// Soft back-off so a persistently-failing broker does not
			// burn CPU. Bounded by ctx.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		if s.dispatch(ctx, reader, km, handler) {
			// A transient handler error left this record's offset
			// uncommitted. Reset the reader so the next fetch rewinds to
			// the last committed offset (before the failed record) — a
			// later success on this partition must not commit past it
			// and silently drop the failed message. See dispatch's doc
			// for the Kafka-watermark rationale.
			s.resetStreak++
			newReader, resetErr := s.resetReader(ctx, reader, b.Exchange)
			// The old reader is closed inside resetReader; clear it so
			// the deferred cleanup does not double-close it.
			reader = newReader
			if resetErr != nil {
				return resetErr
			}
			if newReader == nil {
				// ctx cancelled during the back-off.
				return nil
			}
		} else {
			s.resetStreak = 0
		}
	}
}

// resetReader closes the current reader and constructs a fresh one
// bound to the same topic and group, after a bounded back-off. The new
// reader resumes from the partition's last committed offset, so a
// record whose handler returned a transient error is re-fetched and
// redelivered. Returns (nil, nil) when ctx is cancelled during the
// back-off and (nil, err) when a fresh reader cannot be built.
func (s *Subscriber) resetReader(ctx context.Context, old *kafka.Reader, topic string) (*kafka.Reader, error) {
	if closeErr := old.Close(); closeErr != nil {
		s.logger.Warn("kafkabackend: reader close failed during reset",
			redact.String("topic", topic),
			redact.Error(closeErr),
		)
	}
	// Soft escalating back-off so a persistently-failing handler does not
	// spin consumer-group rebalances. Bounded by ctx; capped at 30s.
	// Base 500ms doubles each consecutive reset (1: 500ms, 2: 1s, …).
	delay := 500 * time.Millisecond
	for i := 1; i < s.resetStreak && delay < 30*time.Second; i++ {
		delay *= 2
	}
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	s.logger.Info("kafkabackend: reader reset after transient handler error",
		redact.String("topic", topic),
		"streak", s.resetStreak,
		"backoff", delay,
	)
	select {
	case <-ctx.Done():
		return nil, nil
	case <-time.After(delay):
	}
	reader, err := s.newReader(topic)
	if err != nil {
		return nil, err
	}
	return reader, nil
}

// ConsumeOnce reads from the topic until ctx is cancelled or an error
// terminates the underlying reader. For kafka-go this is functionally
// equivalent to Consume since the Reader handles reconnection
// internally; provided for messaging.Consumer parity.
func (s *Subscriber) ConsumeOnce(ctx context.Context, b messaging.Binding, handler messaging.Handler) error {
	return s.Consume(ctx, b, handler)
}

func (s *Subscriber) subscribesTo(topic string) bool {
	for _, t := range s.topics {
		if t == topic {
			return true
		}
	}
	return false
}

func (s *Subscriber) newReader(topic string) (*kafka.Reader, error) {
	// Scope the Reader to the single binding topic, NOT the full
	// constructor topic set. A Subscriber may be constructed with many
	// topics, but each Consume call binds one Reader to exactly one
	// topic (the Binding.Exchange) so dispatch only ever sees records
	// for that binding — "let Consume dispatch by Binding.Exchange".
	// Using GroupTopics with the whole topic set would let topic-b
	// records reach a Consume(Binding{Exchange:"a"}) handler, and
	// concurrent Consume calls on one group would receive arbitrary
	// partition assignments across topics.
	//
	// kafka.ReaderConfig requires Topic OR GroupTopics but not both —
	// the single-topic Topic field is kafka-go's idiomatic shape and is
	// fully valid alongside GroupID.
	rc := kafka.ReaderConfig{
		Brokers:           s.cfg.Brokers,
		GroupID:           s.groupID,
		Topic:             topic,
		MinBytes:          s.options.minBytes,
		MaxBytes:          s.options.maxBytes,
		MaxWait:           s.options.maxWait,
		StartOffset:       s.options.startOffset,
		HeartbeatInterval: s.options.heartbeatInterval,
		SessionTimeout:    s.options.sessionTimeout,
		RebalanceTimeout:  s.options.rebalanceTimeout,
		CommitInterval:    s.options.commitInterval,
		QueueCapacity:     s.options.queueCapacity,
		Dialer:            s.options.dialer,
	}
	if err := rc.Validate(); err != nil {
		return nil, redact.WrapError("kafkabackend: reader config", err)
	}
	return kafka.NewReader(rc), nil
}

// committer is the subset of [kafka.Reader] the dispatch path needs to
// acknowledge a record. It exists as a seam so the per-record decision
// logic can be unit-tested without a live broker.
type committer interface {
	CommitMessages(ctx context.Context, msgs ...kafka.Message) error
}

// dispatch processes one fetched record. It returns true when a
// transient (non-permanent) handler error left the offset uncommitted,
// signalling the Consume loop that it MUST reset the reader before
// fetching further records on this partition.
//
// Why the reset is required: Kafka commits are partition watermarks,
// not per-message acks. If the loop simply continued fetching after a
// transient error, a later record on the same partition that succeeds
// would commit an offset PAST the failed record, permanently skipping
// it — turning the documented at-least-once redelivery into at-most-
// once. Resetting the reader rewinds to the last committed offset so
// the failed record is re-fetched and redelivered.
func (s *Subscriber) dispatch(ctx context.Context, reader committer, km kafka.Message, handler messaging.Handler) (retry bool) {
	started := time.Now()
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("kafkabackend: handler panicked — committing offset to skip poison pill",
				redact.String("topic", km.Topic),
				redact.Panic(r),
				slog.String("stack", string(debug.Stack())),
			)
			s.metrics.observeHandler(km.Topic, s.groupID, kafkaHandlerOutcomePanic, started)
			s.commitWithOutcome(ctx, reader, km, kafkaConsumeOutcomeHandlerPanic)
		}
	}()
	delivery, err := fromKafkaMessage(km)
	if err != nil {
		s.logger.Error("kafkabackend: malformed message — committing offset to skip",
			redact.String("topic", km.Topic),
			redact.Error(err),
		)
		s.metrics.observeHandler(km.Topic, s.groupID, kafkaHandlerOutcomeDecodeError, started)
		s.commitWithOutcome(ctx, reader, km, kafkaConsumeOutcomeDecodeError)
		return false
	}
	if err := messaging.ValidateMessage(delivery.Message); err != nil {
		s.logger.Error("kafkabackend: inbound message failed validation — committing offset to skip",
			redact.String("topic", km.Topic),
			redact.String("msg_id", delivery.Message.ID),
			redact.Error(err),
		)
		s.metrics.observeHandler(km.Topic, s.groupID, kafkaHandlerOutcomeValidateError, started)
		s.commitWithOutcome(ctx, reader, km, kafkaConsumeOutcomeValidateError)
		return false
	}
	// Bound handler execution (parity with amqpbackend/natsbackend). Detach
	// from parent cancellation so an in-flight handler can finish within the
	// timeout when shutdown is signalled mid-dispatch.
	handlerCtx, handlerCancel := context.WithTimeout(context.WithoutCancel(ctx), handlerTimeout)
	defer handlerCancel()
	if err := handler(handlerCtx, delivery); err != nil {
		if apperror.IsPermanent(err) {
			s.logger.Error("kafkabackend: permanent error — committing offset to skip poison pill",
				redact.String("topic", km.Topic),
				redact.String("msg_id", delivery.Message.ID),
				redact.Error(err),
			)
			s.metrics.observeHandler(km.Topic, s.groupID, kafkaHandlerOutcomeError, started)
			s.commitWithOutcome(ctx, reader, km, kafkaConsumeOutcomePermanent)
			return false
		}
		s.logger.Warn("kafkabackend: handler returned error — leaving offset uncommitted for redelivery",
			redact.String("topic", km.Topic),
			redact.String("msg_id", delivery.Message.ID),
			redact.Error(err),
		)
		s.metrics.observeHandler(km.Topic, s.groupID, kafkaHandlerOutcomeError, started)
		s.metrics.observeConsumed(km.Topic, s.groupID, kafkaConsumeOutcomeRetry)
		return true
	}
	s.metrics.observeHandler(km.Topic, s.groupID, kafkaHandlerOutcomeSuccess, started)
	s.commitWithOutcome(ctx, reader, km, kafkaConsumeOutcomeAcked)
	return false
}

func (s *Subscriber) commitWithOutcome(ctx context.Context, reader committer, km kafka.Message, outcome string) {
	if err := reader.CommitMessages(ctx, km); err != nil {
		s.logger.Warn("kafkabackend: commit offset failed",
			redact.String("topic", km.Topic),
			redact.Error(err),
		)
		s.metrics.observeConsumed(km.Topic, s.groupID, kafkaConsumeOutcomeCommitFailed)
		return
	}
	s.metrics.observeConsumed(km.Topic, s.groupID, outcome)
}

func buildDialer(cfg Config) (*kafka.Dialer, error) {
	d := &kafka.Dialer{
		Timeout:   10 * time.Second,
		DualStack: true,
		ClientID:  cfg.ClientID,
	}
	if cfg.TLS != nil {
		d.TLS = cfg.TLS
	}
	if cfg.SASLMechanism != "" {
		mech, err := saslMechanism(cfg)
		if err != nil {
			return nil, err
		}
		d.SASLMechanism = mech
	}
	return d, nil
}

// isFatalKafkaFetchError reports fetch errors that will never heal without
// operator intervention (auth/ACL/topic misconfiguration). Transient
// network and rebalance errors continue to retry (review-17).
func isFatalKafkaFetchError(err error) bool {
	if err == nil {
		return false
	}
	var ke kafka.Error
	if errors.As(err, &ke) {
		switch ke {
		case kafka.TopicAuthorizationFailed,
			kafka.GroupAuthorizationFailed,
			kafka.ClusterAuthorizationFailed,
			kafka.SASLAuthenticationFailed,
			kafka.UnknownTopicOrPartition,
			kafka.InvalidTopic,
			kafka.UnsupportedSASLMechanism,
			kafka.IllegalSASLState:
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"sasl authentication failed",
		"topic authorization failed",
		"group authorization failed",
		"cluster authorization failed",
		"unknown topic or partition",
		"invalid topic",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
