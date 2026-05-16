//go:build integration

package amqp

import (
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/integrationtest/v2/rabbitmqtest"
)

// Start returns the AMQP URL of a shared RabbitMQ testcontainer. The
// container is created on the first call and reused for all subsequent calls
// within the same test process.
//
// This is a zero-cost re-export of [rabbitmqtest.Start].
var Start = rabbitmqtest.Start
