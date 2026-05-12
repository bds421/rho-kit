package amqpbackend

import "github.com/bds421/rho-kit/infra/v2/messaging"

// Compile-time interface checks.
var (
	_ messaging.Publisher = (*Publisher)(nil)
	_ messaging.Consumer  = (*Consumer)(nil)
	_ messaging.Connector = (*Connection)(nil)
)
