package amqpbackend

import "github.com/bds421/rho-kit/infra/messaging"

// Compile-time interface checks.
var (
	_ messaging.MessagePublisher = (*Publisher)(nil)
	_ messaging.MessageConsumer  = (*Consumer)(nil)
	_ messaging.Connector        = (*Connection)(nil)
)
