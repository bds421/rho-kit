package amqpbackend

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

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
		return fmt.Errorf("get channel: %w", err)
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
			return fmt.Errorf("declare exchange: %w", err)
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
		return nil, fmt.Errorf("get channel: %w", err)
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
			return nil, fmt.Errorf("declare exchange: %w", err)
		}

		db := messaging.Binding{BindingSpec: b}

		if b.Retry != nil {
			db.RetryExchange = b.Exchange + ".retry"
			db.RetryQueue = b.Queue + ".retry"
			db.DeadExchange = messaging.DeadExchangeName(b.Exchange)
			db.DeadQueue = b.Queue + ".dead"

			if err := declareRetryTopology(ch, b, db); err != nil {
				return nil, err
			}
		}

		var queueArgs amqp.Table
		if b.Retry != nil {
			queueArgs = amqp.Table{
				"x-dead-letter-exchange":    db.RetryExchange,
				"x-dead-letter-routing-key": b.Queue,
			}
		}

		_, err = ch.QueueDeclare(
			b.Queue,
			true,  // durable
			false, // auto-delete
			false, // exclusive
			false, // no-wait
			queueArgs,
		)
		if err != nil {
			return nil, fmt.Errorf("declare queue: %w", err)
		}

		if err := ch.QueueBind(
			b.Queue,
			b.RoutingKey,
			b.Exchange,
			false, // no-wait
			nil,
		); err != nil {
			return nil, fmt.Errorf("bind queue to exchange: %w", err)
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
		return fmt.Errorf("declare retry exchange: %w", err)
	}

	// Retry queue — holds messages for the TTL delay, then dead-letters
	// back to the original exchange with the original routing key.
	_, err := ch.QueueDeclare(
		db.RetryQueue,
		true, false, false, false,
		amqp.Table{
			"x-message-ttl":             int64(b.Retry.Delay / time.Millisecond),
			"x-dead-letter-exchange":    b.Exchange,
			"x-dead-letter-routing-key": b.RoutingKey,
		},
	)
	if err != nil {
		return fmt.Errorf("declare retry queue: %w", err)
	}

	if err := ch.QueueBind(db.RetryQueue, b.Queue, db.RetryExchange, false, nil); err != nil {
		return fmt.Errorf("bind retry queue: %w", err)
	}

	// Dead exchange — routes permanently failed messages.
	if err := ch.ExchangeDeclare(
		db.DeadExchange,
		messaging.ExchangeDirect,
		true, false, false, false, nil,
	); err != nil {
		return fmt.Errorf("declare dead exchange: %w", err)
	}

	// Dead queue — permanent storage for inspection.
	if _, err := ch.QueueDeclare(db.DeadQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dead queue: %w", err)
	}

	if err := ch.QueueBind(db.DeadQueue, b.Queue, db.DeadExchange, false, nil); err != nil {
		return fmt.Errorf("bind dead queue: %w", err)
	}

	return nil
}
