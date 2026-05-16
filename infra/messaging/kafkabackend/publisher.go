package kafkabackend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"github.com/segmentio/kafka-go/sasl/plain"
	"github.com/segmentio/kafka-go/sasl/scram"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// Publisher writes [messaging.Message]s to Kafka topics via a single
// kafka-go [kafka.Writer]. The writer is configured at construction
// time (compression, required acks, batching) and reused across every
// Publish call.
type Publisher struct {
	writer      *kafka.Writer
	logger      *slog.Logger
	sizeLimiter messaging.MessageSizeLimiter
	metrics     *Metrics
}

// PublisherOption configures a [Publisher].
type PublisherOption func(*publisherOptions)

type publisherOptions struct {
	compression            kafka.Compression
	requiredAcks           kafka.RequiredAcks
	batchTimeout           time.Duration
	batchSize              int
	batchBytes             int64
	writeTimeout           time.Duration
	readTimeout            time.Duration
	logger                 *slog.Logger
	sizeLimiter            messaging.MessageSizeLimiter
	metrics                *Metrics
	balancer               kafka.Balancer
	allowAutoTopicCreation bool
}

// WithCompression sets the producer compression codec. Default:
// [kafka.Snappy], chosen for its CPU / ratio balance. Pass
// [kafka.Gzip], [kafka.Lz4], [kafka.Zstd], or 0 (no compression) to
// override.
func WithCompression(codec kafka.Compression) PublisherOption {
	return func(o *publisherOptions) { o.compression = codec }
}

// WithRequiredAcks overrides the producer durability setting. Default:
// [kafka.RequireAll] — wait for the full ISR to acknowledge. Pass
// [kafka.RequireOne] (leader only) or [kafka.RequireNone]
// (fire-and-forget) for higher throughput at the cost of durability.
func WithRequiredAcks(acks kafka.RequiredAcks) PublisherOption {
	return func(o *publisherOptions) { o.requiredAcks = acks }
}

// WithBatchTimeout overrides how long the writer waits before flushing
// an incomplete batch. Default: 10ms — kafka-go's default flushes at
// 1s, which is unnecessarily latent for a synchronous Publish path.
// Values <= 0 fall back to kafka-go's default.
func WithBatchTimeout(d time.Duration) PublisherOption {
	if d < 0 {
		panic("kafkabackend: WithBatchTimeout requires a non-negative duration")
	}
	return func(o *publisherOptions) { o.batchTimeout = d }
}

// WithBatchSize overrides the writer batch-size target. Default: 1 —
// since Publish is synchronous and waits for the batch to flush, a
// per-message batch keeps tail latency predictable. Increase for
// throughput-oriented workloads that publish many messages in parallel
// (kafka-go batches across concurrent WriteMessages calls).
func WithBatchSize(n int) PublisherOption {
	if n <= 0 {
		panic("kafkabackend: WithBatchSize requires n > 0")
	}
	return func(o *publisherOptions) { o.batchSize = n }
}

// WithBatchBytes overrides the writer batch-bytes target (default
// kafka.Writer default = 1 MiB). Bound for the maximum aggregate
// request body the writer will accumulate before flushing.
func WithBatchBytes(n int64) PublisherOption {
	if n <= 0 {
		panic("kafkabackend: WithBatchBytes requires n > 0")
	}
	return func(o *publisherOptions) { o.batchBytes = n }
}

// WithWriteTimeout overrides the kafka.Writer.WriteTimeout. Default
// inherits kafka-go's 10s. Values <= 0 panic to surface
// misconfiguration at startup.
func WithWriteTimeout(d time.Duration) PublisherOption {
	if d <= 0 {
		panic("kafkabackend: WithWriteTimeout requires a positive duration")
	}
	return func(o *publisherOptions) { o.writeTimeout = d }
}

// WithReadTimeout overrides the kafka.Writer.ReadTimeout. Default
// inherits kafka-go's 10s.
func WithReadTimeout(d time.Duration) PublisherOption {
	if d <= 0 {
		panic("kafkabackend: WithReadTimeout requires a positive duration")
	}
	return func(o *publisherOptions) { o.readTimeout = d }
}

// WithBalancer overrides the partition balancer. Default:
// [kafka.Hash] — messages with the same record key (routing key) land
// on the same partition, giving per-key ordering. Pass
// [kafka.RoundRobin] or a custom balancer to opt out.
func WithBalancer(b kafka.Balancer) PublisherOption {
	if b == nil {
		panic("kafkabackend: WithBalancer requires non-nil balancer")
	}
	return func(o *publisherOptions) { o.balancer = b }
}

// WithPublisherLogger overrides the publisher logger. nil normalises
// to [slog.Default].
func WithPublisherLogger(l *slog.Logger) PublisherOption {
	return func(o *publisherOptions) { o.logger = l }
}

// WithMessageSizeLimiter replaces the publisher's message-size policy.
func WithMessageSizeLimiter(l messaging.MessageSizeLimiter) PublisherOption {
	return func(o *publisherOptions) { o.sizeLimiter = l }
}

// WithMaxMessageBytes sets the default serialized message-size limit.
func WithMaxMessageBytes(maxBytes int) PublisherOption {
	return func(o *publisherOptions) {
		o.sizeLimiter = o.sizeLimiter.WithDefaultMaxBytes(maxBytes)
	}
}

// WithoutMaxMessageBytes disables the default size limit. Route-
// specific limits configured with [WithRouteMaxMessageBytes] still
// apply.
func WithoutMaxMessageBytes() PublisherOption {
	return func(o *publisherOptions) {
		o.sizeLimiter = o.sizeLimiter.WithoutDefaultMaxBytes()
	}
}

// WithRouteMaxMessageBytes overrides the message-size limit for one
// exact exchange+routing-key pair. routingKey may be empty.
func WithRouteMaxMessageBytes(exchange, routingKey string, maxBytes int) PublisherOption {
	return func(o *publisherOptions) {
		o.sizeLimiter = o.sizeLimiter.WithRouteMaxBytes(exchange, routingKey, maxBytes)
	}
}

// WithPublisherMetrics attaches Prometheus metrics to the publisher.
func WithPublisherMetrics(m *Metrics) PublisherOption {
	if m == nil {
		panic("kafkabackend: WithPublisherMetrics requires non-nil metrics")
	}
	return func(o *publisherOptions) { o.metrics = m }
}

// WithAllowAutoTopicCreation toggles kafka.Writer.AllowAutoTopicCreation,
// allowing the broker to create missing topics on first publish (when
// the broker is configured to permit it). Default: false. Use only in
// test or trusted-tenant deployments — production topics should be
// declared explicitly so partition counts, retention, and replication
// factor are operator-managed.
func WithAllowAutoTopicCreation(allow bool) PublisherOption {
	return func(o *publisherOptions) { o.allowAutoTopicCreation = allow }
}

// NewPublisher constructs a [Publisher] backed by a kafka.Writer
// addressed at brokers. The publisher writes to dynamic topics; the
// kit-side exchange parameter becomes the Kafka topic at every
// Publish call.
//
// Panics if brokers is empty or a SASL / TLS misconfiguration is
// detected — these are configuration errors that must surface at
// startup.
func NewPublisher(brokers []string, opts ...PublisherOption) (*Publisher, error) {
	cfg := Config{Brokers: brokers}
	return NewPublisherWithConfig(cfg, opts...)
}

// NewPublisherWithConfig is the full-fidelity constructor used when
// callers need TLS or SASL on the wire. It honours every Config field
// and the same option set as [NewPublisher].
func NewPublisherWithConfig(cfg Config, opts ...PublisherOption) (*Publisher, error) {
	cfg, err := cfg.Clone()
	if err != nil {
		return nil, err
	}
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	options := publisherOptions{
		compression:  kafka.Snappy,
		requiredAcks: kafka.RequireAll,
		batchTimeout: 10 * time.Millisecond,
		batchSize:    1,
		sizeLimiter:  messaging.DefaultMessageSizeLimiter(),
		balancer:     &kafka.Hash{},
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("kafkabackend: NewPublisher option must not be nil")
		}
		opt(&options)
	}
	if options.logger == nil {
		options.logger = slog.Default()
	}
	transport, err := buildTransport(cfg)
	if err != nil {
		return nil, err
	}
	writer := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers...),
		Balancer:               options.balancer,
		RequiredAcks:           options.requiredAcks,
		Compression:            options.compression,
		BatchTimeout:           options.batchTimeout,
		BatchSize:              options.batchSize,
		Transport:              transport,
		AllowAutoTopicCreation: options.allowAutoTopicCreation,
	}
	if options.batchBytes > 0 {
		writer.BatchBytes = options.batchBytes
	}
	if options.writeTimeout > 0 {
		writer.WriteTimeout = options.writeTimeout
	}
	if options.readTimeout > 0 {
		writer.ReadTimeout = options.readTimeout
	}
	return &Publisher{
		writer:      writer,
		logger:      options.logger,
		sizeLimiter: options.sizeLimiter,
		metrics:     options.metrics,
	}, nil
}

// Close releases the underlying kafka.Writer. Safe to call multiple
// times — Writer.Close itself is idempotent.
func (p *Publisher) Close() error {
	if p == nil || p.writer == nil {
		return nil
	}
	return p.writer.Close()
}

func (p *Publisher) ready() error {
	if p == nil || p.writer == nil {
		return messaging.ErrInvalidPublisher
	}
	return nil
}

// Publish writes msg to the Kafka topic named by exchange. The
// routingKey is mirrored into the record's Key (driving partition
// assignment) and into the X-Routing-Key header.
//
// Publish blocks until the kafka.Writer has flushed the message and
// the broker has acknowledged it (with RequiredAcks=RequireAll, that
// means the full ISR). A non-nil return therefore guarantees the
// message will not be lost to a broker crash.
func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, msg messaging.Message) error {
	if err := p.ready(); err != nil {
		return err
	}
	if err := messaging.ValidatePublishContext(ctx); err != nil {
		return err
	}
	if err := messaging.ValidatePublishRoute(exchange, routingKey); err != nil {
		return err
	}
	started := time.Now()
	outcome := kafkaPublishOutcomeFailed
	defer func() {
		p.metrics.observePublish(exchange, routingKey, outcome, started)
	}()
	if err := messaging.ValidateMessage(msg); err != nil {
		outcome = kafkaPublishOutcomeInvalidMessage
		return err
	}
	if err := p.sizeLimiter.Check(exchange, routingKey, msg); err != nil {
		outcome = publishOutcomeForError(err)
		return err
	}
	km, err := toKafkaMessage(exchange, routingKey, msg)
	if err != nil {
		return err
	}
	if err := p.writer.WriteMessages(ctx, km); err != nil {
		return fmt.Errorf("kafkabackend: write: %w", err)
	}
	outcome = kafkaPublishOutcomeSuccess
	return nil
}

func publishOutcomeForError(err error) string {
	if errors.Is(err, messaging.ErrMessageTooLarge) {
		return kafkaPublishOutcomeTooLarge
	}
	return kafkaPublishOutcomeFailed
}

// buildTransport assembles the kafka-go Transport from Config TLS /
// SASL state. Returning a configured *kafka.Transport keeps the SASL
// state isolated to this backend so the writer and reader can share a
// single Transport without leaking implementation detail.
func buildTransport(cfg Config) (*kafka.Transport, error) {
	t := &kafka.Transport{
		ClientID: cfg.ClientID,
	}
	if cfg.TLS != nil {
		t.TLS = cfg.TLS
	}
	if cfg.SASLMechanism != "" {
		mech, err := saslMechanism(cfg)
		if err != nil {
			return nil, err
		}
		t.SASL = mech
	}
	return t, nil
}

func saslMechanism(cfg Config) (sasl.Mechanism, error) {
	switch upper := strings.ToUpper(cfg.SASLMechanism); upper {
	case "PLAIN":
		return plain.Mechanism{Username: cfg.SASLUsername, Password: cfg.SASLPassword}, nil
	case "SCRAM-SHA-256":
		mech, err := scram.Mechanism(scram.SHA256, cfg.SASLUsername, cfg.SASLPassword)
		if err != nil {
			return nil, fmt.Errorf("kafkabackend: SCRAM-SHA-256: %w", err)
		}
		return mech, nil
	case "SCRAM-SHA-512":
		mech, err := scram.Mechanism(scram.SHA512, cfg.SASLUsername, cfg.SASLPassword)
		if err != nil {
			return nil, fmt.Errorf("kafkabackend: SCRAM-SHA-512: %w", err)
		}
		return mech, nil
	default:
		return nil, fmt.Errorf("kafkabackend: unsupported SASL mechanism %q", cfg.SASLMechanism)
	}
}
