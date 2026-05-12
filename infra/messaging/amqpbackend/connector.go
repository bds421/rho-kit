package amqpbackend

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Connector is the AMQP-specific connection interface. It extends the generic
// messaging.Connector with channel access needed by publisher, consumer, and
// topology functions. The concrete [Connection] type implements this interface.
// Define test fakes against Connector to unit-test publishers and consumers
// without a real broker.
type Connector interface {
	// Channel opens a new AMQP channel on the underlying connection.
	Channel() (*amqp.Channel, error)

	// Healthy reports whether the broker connection is alive and usable.
	Healthy() bool

	// Stop shuts down the connection and releases resources. See
	// [messaging.Connector.Stop] for ctx semantics.
	Stop(ctx context.Context) error
}
