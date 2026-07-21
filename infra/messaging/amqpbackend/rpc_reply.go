package amqpbackend

import (
	"context"
	"fmt"
	"strings"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// ChannelProvider provides raw AMQP channels. Connection implements this.
type ChannelProvider interface {
	Channel() (*amqp.Channel, error)
}

// ReplyToAllowFunc decides whether a requester-controlled ReplyTo queue
// name is an acceptable RPC reply destination. Returning false causes
// [ReplySender.Send] to reject the publish without touching the broker.
type ReplyToAllowFunc func(replyTo string) bool

// ReplySender manages a cached AMQP channel for publishing RPC replies.
// It is goroutine-safe and recovers from closed channels by opening new ones.
type ReplySender struct {
	chanProv     ChannelProvider
	mu           sync.Mutex
	ch           *amqp.Channel
	allowReplyTo ReplyToAllowFunc
}

// NewReplySender creates a ReplySender backed by the given channel provider.
func NewReplySender(chanProv ChannelProvider, opts ...ReplySenderOption) *ReplySender {
	rs := &ReplySender{
		chanProv:     chanProv,
		allowReplyTo: defaultReplyToAllow,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(rs)
		}
	}
	return rs
}

// ReplySenderOption configures a [ReplySender].
type ReplySenderOption func(*ReplySender)

// WithReplyToAllow replaces the default ReplyTo allow predicate. Use this
// when callers need custom exclusive reply-queue naming beyond the
// RabbitMQ amq.gen-* / amq.rabbitmq.reply-to conventions. The predicate
// is consulted only after [messaging.ValidateRoutingKey] accepts the name.
func WithReplyToAllow(fn ReplyToAllowFunc) ReplySenderOption {
	if fn == nil {
		panic("amqpbackend: WithReplyToAllow requires a non-nil predicate")
	}
	return func(rs *ReplySender) { rs.allowReplyTo = fn }
}

// defaultReplyToAllow admits RabbitMQ direct-reply-to and server-named
// exclusive reply queues. Arbitrary application queue names are rejected
// so a compromised/malicious requester cannot use the service as a
// confused deputy to inject messages into internal queues via ReplyTo.
func defaultReplyToAllow(replyTo string) bool {
	if replyTo == "amq.rabbitmq.reply-to" {
		return true
	}
	return strings.HasPrefix(replyTo, "amq.gen-")
}

// validateReplyTo checks the requester-controlled ReplyTo queue name
// before publishing on the default exchange.
func (rs *ReplySender) validateReplyTo(replyTo string) error {
	if err := messaging.ValidateRoutingKey(replyTo); err != nil {
		return fmt.Errorf("amqpbackend: invalid ReplyTo: %w", err)
	}
	if replyTo == "" {
		return nil
	}
	// Wildcards are never valid queue names for a direct default-exchange
	// publish and would only enable broad routing mistakes.
	if strings.ContainsAny(replyTo, "*#") {
		return fmt.Errorf("amqpbackend: invalid ReplyTo: must not contain AMQP wildcards")
	}
	allow := rs.allowReplyTo
	if allow == nil {
		allow = defaultReplyToAllow
	}
	if !allow(replyTo) {
		return fmt.Errorf("amqpbackend: ReplyTo %q is not an allowed reply queue (use amq.gen-* / amq.rabbitmq.reply-to or WithReplyToAllow)", replyTo)
	}
	return nil
}

// Send publishes a JSON reply to the delivery's ReplyTo queue.
// If ReplyTo is empty, it is a no-op. Safe for concurrent use.
//
// ReplyTo is treated as untrusted input: it is validated and must pass
// the configured allow predicate before any publish occurs.
func (rs *ReplySender) Send(ctx context.Context, d messaging.Delivery, body []byte) error {
	if d.ReplyTo == "" {
		return nil
	}
	if err := rs.validateReplyTo(d.ReplyTo); err != nil {
		return err
	}

	// Hold the mutex only while obtaining/resetting the cached channel so
	// concurrent RPC replies are not serialised on the network publish.
	rs.mu.Lock()
	ch, err := rs.channelLocked()
	rs.mu.Unlock()
	if err != nil {
		return redact.WrapError("get channel for RPC reply", err)
	}

	if err := ch.PublishWithContext(ctx,
		"",        // default exchange
		d.ReplyTo, // direct to reply queue
		false,
		false,
		amqp.Publishing{
			ContentType:   "application/json",
			CorrelationId: d.CorrelationID,
			Body:          body,
		},
	); err != nil {
		rs.mu.Lock()
		// Only reset if the channel we used is still the cached one.
		if rs.ch == ch {
			rs.resetLocked()
		}
		rs.mu.Unlock()
		return redact.WrapError("publish RPC reply", err)
	}

	return nil
}

// Close releases the cached channel. Safe to call multiple times.
func (rs *ReplySender) Close() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.resetLocked()
}

// channelLocked returns the cached channel, creating one if needed.
// Caller must hold rs.mu.
func (rs *ReplySender) channelLocked() (*amqp.Channel, error) {
	if rs.ch != nil && !rs.ch.IsClosed() {
		return rs.ch, nil
	}

	ch, err := rs.chanProv.Channel()
	if err != nil {
		return nil, err
	}

	rs.ch = ch
	return ch, nil
}

// resetLocked closes and nils the cached channel.
// Caller must hold rs.mu.
func (rs *ReplySender) resetLocked() {
	if rs.ch != nil {
		_ = rs.ch.Close()
		rs.ch = nil
	}
}
