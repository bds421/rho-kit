package messaging

// Connector represents a connection to a message broker. Backend-specific
// implementations (amqpbackend.Connection, etc.) satisfy this interface.
//
// In the app package, Connector is the type of Infrastructure.Broker,
// allowing the builder to pass the concrete connection while keeping
// handler code decoupled from the transport.
type Connector interface {
	// Healthy reports whether the broker connection is alive and usable.
	Healthy() bool

	// Close shuts down the connection and releases resources.
	Close() error
}
