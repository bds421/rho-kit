package amqpbackend

import (
	"errors"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// DeclareExchanges declares exchanges without creating queues or bindings.
func DeclareExchanges(conn Connector, specs ...messaging.ExchangeSpec) error {
	if len(specs) == 0 {
		return nil
	}
	for _, s := range specs {
		if err := messaging.ValidateExchangeName(s.Exchange); err != nil {
			return err
		}
		switch s.ExchangeType {
		case messaging.ExchangeDirect, messaging.ExchangeFanout, messaging.ExchangeTopic, messaging.ExchangeHeaders:
		default:
			return errors.New("unsupported exchange type")
		}
	}
	ch, err := conn.Channel()
	if err != nil {
		return redact.WrapError("get channel", err)
	}
	defer func() { _ = ch.Close() }()

	for _, s := range specs {
		if err := ch.ExchangeDeclare(
			s.Exchange,
			s.ExchangeType,
			true,  // durable
			false, // auto-delete
			false, // internal
			false, // no-wait
			nil,
		); err != nil {
			return redact.WrapError("declare exchange", err)
		}
	}
	return nil
}

// DeclareTopology creates the exchange, queue, and binding described by b.
// Both the exchange and queue are declared as durable and non-auto-delete.
// For multiple bindings, prefer DeclareAll to reuse a single channel.
func DeclareTopology(conn Connector, b messaging.BindingSpec) (messaging.Binding, error) {
	declared, err := DeclareAll(conn, b)
	if err != nil {
		return messaging.Binding{}, err
	}
	return declared[0], nil
}

// DeclareAll declares all provided bindings on a single AMQP channel.
// Returns a Binding for each input, with computed retry/dead names.
//
// Bindings are normalized before validation: when Retry is nil and
// WithoutRetry is false, [messaging.DefaultRetryPolicy] is applied and a
// slog.Default warning is emitted. Set WithoutRetry=true on the BindingSpec
// to opt out and keep ack-and-discard semantics.
func DeclareAll(conn Connector, bindings ...messaging.BindingSpec) ([]messaging.Binding, error) {
	bindings = messaging.CloneBindingSpecs(bindings)
	for _, w := range messaging.NormalizeBindingSpecs(bindings) {
		slog.Default().Warn("amqpbackend: " + w)
	}
	if err := messaging.ValidateBindingSpecs(bindings); err != nil {
		return nil, err
	}

	ch, err := conn.Channel()
	if err != nil {
		return nil, redact.WrapError("get channel", err)
	}
	defer func() { _ = ch.Close() }()

	result := make([]messaging.Binding, 0, len(bindings))

	for _, b := range bindings {
		if err := ch.ExchangeDeclare(
			b.Exchange,
			b.ExchangeType,
			true,  // durable
			false, // auto-delete
			false, // internal
			false, // no-wait
			nil,
		); err != nil {
			return nil, redact.WrapError("declare exchange", err)
		}

		db := messaging.Binding{BindingSpec: b}

		if b.Retry != nil {
			db.RetryExchange = b.Exchange + ".retry"
			db.RetryQueue = b.ConsumerGroup + ".retry"
			db.DeadExchange = messaging.DeadExchangeName(b.Exchange)
			db.DeadQueue = b.ConsumerGroup + ".dead"

			if err := declareRetryTopology(ch, b, db); err != nil {
				return nil, err
			}
		}

		var queueArgs amqp.Table
		if b.Retry != nil {
			queueArgs = amqp.Table{
				"x-dead-letter-exchange":    db.RetryExchange,
				"x-dead-letter-routing-key": b.ConsumerGroup,
			}
		}

		_, err = ch.QueueDeclare(
			b.ConsumerGroup,
			true,  // durable
			false, // auto-delete
			false, // exclusive
			false, // no-wait
			queueArgs,
		)
		if err != nil {
			return nil, redact.WrapError("declare queue", err)
		}

		if err := ch.QueueBind(
			b.ConsumerGroup,
			b.RoutingKey,
			b.Exchange,
			false, // no-wait
			nil,
		); err != nil {
			return nil, redact.WrapError("bind queue to exchange", err)
		}

		result = append(result, db)
	}

	return result, nil
}

// declareRetryTopology creates the retry exchange, retry queue, dead exchange,
// and dead queue using the pre-computed names from the Binding.
func declareRetryTopology(ch *amqp.Channel, b messaging.BindingSpec, db messaging.Binding) error {
	// Retry exchange — routes nacked messages to the retry queue.
	if err := ch.ExchangeDeclare(
		db.RetryExchange,
		messaging.ExchangeDirect,
		true, false, false, false, nil,
	); err != nil {
		return redact.WrapError("declare retry exchange", err)
	}

	// Retry queue — holds messages for the TTL delay, then dead-letters
	// straight back to the originating queue (see retryQueueArgs).
	_, err := ch.QueueDeclare(
		db.RetryQueue,
		true, false, false, false,
		retryQueueArgs(b),
	)
	if err != nil {
		return redact.WrapError("declare retry queue", err)
	}

	if err := ch.QueueBind(db.RetryQueue, b.ConsumerGroup, db.RetryExchange, false, nil); err != nil {
		return redact.WrapError("bind retry queue", err)
	}

	// Dead exchange — routes permanently failed messages.
	if err := ch.ExchangeDeclare(
		db.DeadExchange,
		messaging.ExchangeDirect,
		true, false, false, false, nil,
	); err != nil {
		return redact.WrapError("declare dead exchange", err)
	}

	// Dead queue — permanent storage for inspection.
	if _, err := ch.QueueDeclare(db.DeadQueue, true, false, false, false, nil); err != nil {
		return redact.WrapError("declare dead queue", err)
	}

	if err := ch.QueueBind(db.DeadQueue, b.ConsumerGroup, db.DeadExchange, false, nil); err != nil {
		return redact.WrapError("bind dead queue", err)
	}

	return nil
}

// retryQueueArgs builds the declaration arguments for the retry queue.
//
// When a TTL'd retry message expires it is dead-lettered back to the
// originating consumer-group queue via the AMQP default exchange ("").
// The default exchange routes by routing key equal to the queue name, so
// x-dead-letter-routing-key is set to b.ConsumerGroup (the main queue).
//
// Earlier this dead-lettered through the original exchange (b.Exchange)
// with the binding key (b.RoutingKey). That republished the retried message
// into EVERY queue bound with a matching key — for a fanout exchange every
// sibling consumer group reprocessed the message on each retry bounce of a
// single group. Routing via the default exchange targets only the queue
// that originally failed, so siblings never see duplicates.
//
// It also fixes the routing key surfaced to handlers: the binding key for a
// topic exchange is a pattern (e.g. "orders.*") and for fanout is empty, so
// the previous scheme rewrote messaging.Delivery.RoutingKey on retried
// deliveries. The original publish key remains available in the x-death
// header's routing-keys field for handlers that need it.
func retryQueueArgs(b messaging.BindingSpec) amqp.Table {
	// RabbitMQ TTLs are whole milliseconds. Floor sub-millisecond delays
	// at 1ms so truncation to 0 cannot create an immediate-bounce loop.
	ttlMillis := int64(b.Retry.Delay / time.Millisecond)
	if b.Retry.Delay > 0 && ttlMillis < 1 {
		ttlMillis = 1
	}
	return amqp.Table{
		"x-message-ttl":             ttlMillis,
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": b.ConsumerGroup,
	}
}
