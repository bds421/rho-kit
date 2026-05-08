package redisbackend

import "github.com/bds421/rho-kit/infra/v2/messaging"

// Compile-time interface checks.
var (
	_ messaging.MessagePublisher = (*Publisher)(nil)
	_ messaging.MessageConsumer  = (*Consumer)(nil)
)
