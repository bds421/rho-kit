// Package natsbackend implements a NATS JetStream-backed
// [messaging.MessagePublisher] and consumer.
//
// JetStream gives the kit:
//
//   - Persistence — messages survive a broker restart.
//   - Acknowledgements — Publish returns only after the broker has
//     accepted+stored the message.
//   - Pull consumers with explicit ack — durable consumer state
//     tracks per-message ack status across restarts.
//
// Use this backend when:
//   - You need higher throughput than single-node RabbitMQ can deliver.
//   - You don't want the operational overhead of Kafka.
//   - Your consumers can tolerate at-least-once delivery semantics
//     (deduplicate at the application layer if exactly-once is needed).
//
// The translation between [messaging.Message] and NATS JetStream:
//
//   - Stream subject = `exchange + "." + routingKey` when routingKey
//     is non-empty, otherwise just `exchange`. The dotted subject form
//     mirrors NATS conventions and keeps wildcard subscriptions
//     workable.
//   - Message body = JSON-encoded [messaging.Message] (same shape used
//     by the AMQP and Redis backends).
//   - Headers ride the NATS Msg.Header map.
package natsbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/bds421/rho-kit/infra/messaging"
)

// Config is the connection-level configuration. Stream/consumer
// declarations live on [StreamConfig] and [ConsumerConfig] so a single
// connection can serve multiple streams.
type Config struct {
	URL string // e.g. "nats://localhost:4222"

	// Name identifies this client in NATS introspection. Defaults to
	// "rho-kit".
	Name string

	// PublishAckWait caps how long a synchronous Publish waits for the
	// JetStream broker ack. Default: 5s.
	PublishAckWait time.Duration

	// MaxReconnects bounds reconnection attempts before NATS gives up.
	// -1 means infinite. Default: -1.
	MaxReconnects int

	// ReconnectWait is the back-off between reconnect attempts.
	// Default: 2s.
	ReconnectWait time.Duration
}

// Connection holds an open nats.Conn and its JetStream context. Use
// [Connect] to construct.
type Connection struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// Connect dials NATS and returns a Connection. The connection
// auto-reconnects on transient failures; callers do not need to wrap
// it in a retry loop.
func Connect(ctx context.Context, cfg Config) (*Connection, error) {
	if cfg.URL == "" {
		return nil, errors.New("natsbackend: URL must not be empty")
	}
	if cfg.Name == "" {
		cfg.Name = "rho-kit"
	}
	if cfg.MaxReconnects == 0 {
		cfg.MaxReconnects = -1 // infinite
	}
	if cfg.ReconnectWait <= 0 {
		cfg.ReconnectWait = 2 * time.Second
	}

	nc, err := nats.Connect(cfg.URL,
		nats.Name(cfg.Name),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.ReconnectWait(cfg.ReconnectWait),
	)
	if err != nil {
		return nil, fmt.Errorf("natsbackend: connect: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("natsbackend: jetstream: %w", err)
	}
	return &Connection{nc: nc, js: js}, nil
}

// Healthy reports whether the underlying NATS connection is currently
// connected. Suitable for [messaging.Connector].
func (c *Connection) Healthy() bool {
	return c.nc != nil && c.nc.IsConnected()
}

// Close drains pending publishes and closes the connection. Drain is
// best-effort with a 5s deadline.
func (c *Connection) Close() error {
	if c.nc == nil {
		return nil
	}
	return c.nc.Drain()
}

// JetStream returns the raw JetStream context for callers needing
// features the kit doesn't expose. Use sparingly.
func (c *Connection) JetStream() jetstream.JetStream { return c.js }

// StreamConfig declares a JetStream stream's persistence policy.
type StreamConfig struct {
	Name        string
	Subjects    []string // e.g. ["events.>"]
	MaxAge      time.Duration
	MaxBytes    int64
	Retention   jetstream.RetentionPolicy // default: LimitsPolicy
	StorageType jetstream.StorageType     // default: FileStorage
}

// EnsureStream creates or updates the stream described by cfg.
// Idempotent — safe to call on every startup.
func (c *Connection) EnsureStream(ctx context.Context, cfg StreamConfig) error {
	if cfg.Name == "" {
		return errors.New("natsbackend: StreamConfig.Name required")
	}
	if len(cfg.Subjects) == 0 {
		return errors.New("natsbackend: StreamConfig.Subjects required")
	}
	jcfg := jetstream.StreamConfig{
		Name:      cfg.Name,
		Subjects:  cfg.Subjects,
		MaxAge:    cfg.MaxAge,
		MaxBytes:  cfg.MaxBytes,
		Retention: cfg.Retention,
		Storage:   cfg.StorageType,
	}
	_, err := c.js.CreateOrUpdateStream(ctx, jcfg)
	if err != nil {
		return fmt.Errorf("natsbackend: ensure stream %q: %w", cfg.Name, err)
	}
	return nil
}

// Publisher publishes [messaging.Message]s to JetStream.
type Publisher struct {
	conn *Connection
	wait time.Duration
}

// NewPublisher returns a Publisher backed by conn.
func NewPublisher(conn *Connection) *Publisher {
	if conn == nil {
		panic("natsbackend: Publisher requires a Connection")
	}
	return &Publisher{conn: conn, wait: 5 * time.Second}
}

// Publish satisfies [messaging.MessagePublisher].
//
// The NATS subject is `exchange + "." + routingKey` (or just
// `exchange` when routingKey is empty). Returns only after the
// JetStream broker confirms storage, so a non-nil return guarantees
// the message will not be lost to a broker crash.
func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, msg messaging.Message) error {
	subject := composeSubject(exchange, routingKey)
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("natsbackend: marshal message: %w", err)
	}
	natsMsg := &nats.Msg{
		Subject: subject,
		Data:    body,
		Header:  nats.Header{},
	}
	for k, v := range msg.Headers {
		natsMsg.Header.Set(k, v)
	}
	natsMsg.Header.Set("X-Message-Id", msg.ID)
	natsMsg.Header.Set("X-Message-Type", msg.Type)

	pubCtx := ctx
	if p.wait > 0 {
		var cancel context.CancelFunc
		pubCtx, cancel = context.WithTimeout(ctx, p.wait)
		defer cancel()
	}
	_, err = p.conn.js.PublishMsg(pubCtx, natsMsg)
	if err != nil {
		return fmt.Errorf("natsbackend: publish %q: %w", subject, err)
	}
	return nil
}

// ConsumerConfig declares a durable JetStream consumer. The kit
// represents one consumer per (stream, durable name) tuple — the
// durable name pins consumer position across restarts.
type ConsumerConfig struct {
	Stream        string
	Durable       string
	FilterSubject string        // optional — restrict to a subject within the stream
	MaxAckPending int           // default: 256
	AckWait       time.Duration // default: 30s
	// MaxDeliver caps how many times JetStream will redeliver a single
	// message before giving up. Without a cap (the JetStream default of
	// -1 meaning unlimited), a message that reliably triggers a panic
	// in the handler — or any other non-Term failure — Naks forever and
	// blocks the consumer's progress. Default: 5. Set negative to
	// explicitly opt into unlimited redelivery.
	MaxDeliver int
}

// Consumer pulls messages from a JetStream durable consumer and
// dispatches them to a handler. One Consumer instance per
// (stream, durable).
type Consumer struct {
	conn   *Connection
	cfg    ConsumerConfig
	logger *slog.Logger
}

// NewConsumer constructs a Consumer. The underlying durable consumer
// is created lazily on the first [Consumer.Consume] call so callers
// don't pay the round trip during DI wiring.
func NewConsumer(conn *Connection, cfg ConsumerConfig, logger *slog.Logger) *Consumer {
	if conn == nil {
		panic("natsbackend: Consumer requires a Connection")
	}
	if cfg.Stream == "" || cfg.Durable == "" {
		panic("natsbackend: ConsumerConfig requires Stream and Durable")
	}
	if cfg.MaxAckPending <= 0 {
		cfg.MaxAckPending = 256
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = 30 * time.Second
	}
	if cfg.MaxDeliver == 0 {
		cfg.MaxDeliver = 5
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Consumer{conn: conn, cfg: cfg, logger: logger}
}

// Consume blocks until ctx cancels, dispatching messages to handler.
// Returning nil from handler acks; returning an error nacks (the
// message is redelivered after AckWait).
func (c *Consumer) Consume(ctx context.Context, handler messaging.Handler) error {
	if handler == nil {
		return errors.New("natsbackend: handler must not be nil")
	}

	cons, err := c.conn.js.CreateOrUpdateConsumer(ctx, c.cfg.Stream, jetstream.ConsumerConfig{
		Durable:       c.cfg.Durable,
		FilterSubject: c.cfg.FilterSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxAckPending: c.cfg.MaxAckPending,
		AckWait:       c.cfg.AckWait,
		MaxDeliver:    c.cfg.MaxDeliver,
	})
	if err != nil {
		return fmt.Errorf("natsbackend: ensure consumer: %w", err)
	}

	var stopped atomic.Bool
	consume, err := cons.Consume(func(jm jetstream.Msg) {
		if stopped.Load() {
			return
		}
		c.dispatch(ctx, jm, handler)
	})
	if err != nil {
		return fmt.Errorf("natsbackend: start consume: %w", err)
	}

	<-ctx.Done()
	stopped.Store(true)
	consume.Stop()
	return nil
}

// dispatch routes one delivery to the handler and ack/nacks based on
// the result. A handler panic counts as an error — the message is
// nacked so it redelivers up to ConsumerConfig.MaxDeliver times, after
// which JetStream gives up (sends to the configured DLQ if any, else
// drops). The panic is recovered here and not re-raised; the consumer
// must keep running so other deliveries continue to be processed.
// Process-level reaction to handler panics should subscribe to the
// "natsbackend: handler panicked" log line, not rely on a re-throw.
func (c *Consumer) dispatch(ctx context.Context, jm jetstream.Msg, handler messaging.Handler) {
	defer func() {
		if r := recover(); r != nil {
			_ = jm.Nak()
			c.logger.Error("natsbackend: handler panicked",
				slog.Any("panic", r),
			)
		}
	}()

	subject := jm.Subject()
	exchange, routingKey := splitSubject(subject)

	var msg messaging.Message
	if err := json.Unmarshal(jm.Data(), &msg); err != nil {
		c.logger.Error("natsbackend: malformed message — discarding",
			slog.String("subject", subject),
			slog.Any("error", err),
		)
		_ = jm.Term() // unrecoverable — Term tells JetStream not to redeliver
		return
	}

	headers := make(map[string]any, len(jm.Headers()))
	for k, v := range jm.Headers() {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	// Surface JetStream-level redelivery via metadata.
	redelivered := false
	if md, err := jm.Metadata(); err == nil {
		redelivered = md.NumDelivered > 1
	}

	delivery := messaging.Delivery{
		Message:       msg,
		Exchange:      exchange,
		RoutingKey:    routingKey,
		SchemaVersion: msg.SchemaVersion,
		Redelivered:   redelivered,
		Headers:       headers,
	}

	if err := handler(ctx, delivery); err != nil {
		c.logger.Warn("natsbackend: handler returned error — nacking",
			slog.String("subject", subject),
			slog.String("msg_id", msg.ID),
			slog.Any("error", err),
		)
		_ = jm.Nak()
		return
	}
	_ = jm.Ack()
}

func composeSubject(exchange, routingKey string) string {
	if routingKey == "" {
		return exchange
	}
	return exchange + "." + routingKey
}

func splitSubject(subject string) (exchange, routingKey string) {
	i := strings.IndexByte(subject, '.')
	if i < 0 {
		return subject, ""
	}
	return subject[:i], subject[i+1:]
}
