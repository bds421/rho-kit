package messaging

import "context"

// Connector represents a connection to a message broker. Backend-specific
// implementations (amqpbackend.Connection, etc.) satisfy this interface.
//
// In the app package, Connector is the type of Infrastructure.Broker,
// allowing the builder to pass the concrete connection while keeping
// handler code decoupled from the transport.
type Connector interface {
	// Healthy reports whether the broker connection is alive and usable.
	Healthy() bool

	// Stop shuts down the connection and releases resources. The deadline
	// from ctx bounds any backend-side drain (e.g. waiting for in-flight
	// publishes to ack). Backends that cannot honour a deadline must close
	// synchronously and return.
	Stop(ctx context.Context) error
}
