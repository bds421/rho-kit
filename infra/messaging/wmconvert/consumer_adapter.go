package wmconvert

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/bds421/rho-kit/infra/messaging"
)

// ConsumerAdapter wraps a Watermill Subscriber to implement messaging.MessageConsumer.
// It converts Watermill messages to rho-kit Deliveries and manages the Ack/Nack
// protocol based on the handler's error return.
type ConsumerAdapter struct {
	subscriber message.Subscriber
	logger     *slog.Logger
}

// NewConsumerAdapter creates a ConsumerAdapter backed by the given Watermill subscriber.
func NewConsumerAdapter(sub message.Subscriber, logger *slog.Logger) *ConsumerAdapter {
	if sub == nil {
		panic("wmconvert: subscriber must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ConsumerAdapter{
		subscriber: sub,
		logger:     logger,
	}
}

// Consume subscribes to the binding's queue and dispatches messages to the handler.
// It blocks until ctx is cancelled. Implements messaging.MessageConsumer.
//
// The Watermill subscriber handles reconnection internally. The binding's queue
// name is used as the Watermill topic. Exchange and routing key are extracted
// from the message metadata.
func (a *ConsumerAdapter) Consume(ctx context.Context, b messaging.Binding, handler messaging.Handler) error {
	return a.ConsumeOnce(ctx, b, handler)
}

// ConsumeOnce subscribes and processes messages until ctx is cancelled or the
// subscriber channel closes. Implements messaging.MessageConsumer.
func (a *ConsumerAdapter) ConsumeOnce(ctx context.Context, b messaging.Binding, handler messaging.Handler) error {
	msgs, err := a.subscriber.Subscribe(ctx, b.Queue)
	if err != nil {
		return fmt.Errorf("wmconvert subscribe %q: %w", b.Queue, err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case wmMsg, ok := <-msgs:
			if !ok {
				return nil
			}
			a.handleMessage(ctx, wmMsg, b, handler)
		}
	}
}

func (a *ConsumerAdapter) handleMessage(ctx context.Context, wmMsg *message.Message, b messaging.Binding, handler messaging.Handler) {
	delivery, err := ToDelivery(wmMsg)
	if err != nil {
		a.logger.Error("wmconvert: failed to convert message to delivery",
			"error", err,
			"queue", b.Queue,
		)
		wmMsg.Ack()
		return
	}

	// Fill in binding-level metadata if not already in the message.
	if delivery.Exchange == "" {
		delivery.Exchange = b.Exchange
	}
	if delivery.RoutingKey == "" {
		delivery.RoutingKey = b.RoutingKey
	}

	if handlerErr := handler(ctx, delivery); handlerErr != nil {
		a.logger.Warn("wmconvert: handler returned error",
			"queue", b.Queue,
			"msg_id", delivery.Message.ID,
			"error", handlerErr,
		)
		wmMsg.Nack()
		return
	}

	wmMsg.Ack()
}

// Close closes the underlying Watermill subscriber.
func (a *ConsumerAdapter) Close() error {
	return a.subscriber.Close()
}
