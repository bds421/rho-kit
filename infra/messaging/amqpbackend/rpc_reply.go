package amqpbackend

import (
	"context"
	"fmt"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/infra/messaging"
)

// ChannelProvider provides raw AMQP channels. Connection implements this.
type ChannelProvider interface {
	Channel() (*amqp.Channel, error)
}

// ReplySender manages a cached AMQP channel for publishing RPC replies.
// It is goroutine-safe and recovers from closed channels by opening new ones.
type ReplySender struct {
	chanProv ChannelProvider
	mu       sync.Mutex
	ch       *amqp.Channel
}

// NewReplySender creates a ReplySender backed by the given channel provider.
func NewReplySender(chanProv ChannelProvider) *ReplySender {
	return &ReplySender{chanProv: chanProv}
}

// Send publishes a JSON reply to the delivery's ReplyTo queue.
// If ReplyTo is empty, it is a no-op. Safe for concurrent use.
func (rs *ReplySender) Send(ctx context.Context, d messaging.Delivery, body []byte) error {
	if d.ReplyTo == "" {
		return nil
	}

	rs.mu.Lock()
	defer rs.mu.Unlock()

	ch, err := rs.channelLocked()
	if err != nil {
		return fmt.Errorf("get channel for RPC reply: %w", err)
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
		rs.resetLocked()
		return fmt.Errorf("publish RPC reply: %w", err)
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
