package amqpbackend

import (
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/infra/messaging"
)

// DeclareExchanges declares exchanges without creating queues or bindings.
func DeclareExchanges(conn Connector, specs ...messaging.ExchangeSpec) error {
	if len(specs) == 0 {
		return nil
	}
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("get channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	for _, s := range specs {
		if s.Exchange == "" {
			return fmt.Errorf("exchange name must not be empty")
		}
		if err := ch.ExchangeDeclare(
			s.Exchange,
			s.ExchangeType,
			true,  // durable
			false, // auto-delete
			false, // internal
			false, // no-wait
			nil,
		); err != nil {
			return fmt.Errorf("declare exchange %q: %w", s.Exchange, err)
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
func DeclareAll(conn Connector, bindings ...messaging.BindingSpec) ([]messaging.Binding, error) {
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
			return nil, fmt.Errorf("declare exchange %q: %w", b.Exchange, err)
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
			return nil, fmt.Errorf("declare queue %q: %w", b.Queue, err)
		}

		if err := ch.QueueBind(
			b.Queue,
			b.RoutingKey,
			b.Exchange,
			false, // no-wait
			nil,
		); err != nil {
			return nil, fmt.Errorf("bind queue %q to exchange %q: %w", b.Queue, b.Exchange, err)
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
		return fmt.Errorf("declare retry exchange %q: %w", db.RetryExchange, err)
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
		return fmt.Errorf("declare retry queue %q: %w", db.RetryQueue, err)
	}

	if err := ch.QueueBind(db.RetryQueue, b.Queue, db.RetryExchange, false, nil); err != nil {
		return fmt.Errorf("bind retry queue %q: %w", db.RetryQueue, err)
	}

	// Dead exchange — routes permanently failed messages.
	if err := ch.ExchangeDeclare(
		db.DeadExchange,
		messaging.ExchangeDirect,
		true, false, false, false, nil,
	); err != nil {
		return fmt.Errorf("declare dead exchange %q: %w", db.DeadExchange, err)
	}

	// Dead queue — permanent storage for inspection.
	if _, err := ch.QueueDeclare(db.DeadQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dead queue %q: %w", db.DeadQueue, err)
	}

	if err := ch.QueueBind(db.DeadQueue, b.Queue, db.DeadExchange, false, nil); err != nil {
		return fmt.Errorf("bind dead queue %q: %w", db.DeadQueue, err)
	}

	return nil
}
