//go:build integration

package amqpbackend_test

import (
	"log/slog"
	"testing"

	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2"
)

func dialLocalRabbitMQ(t *testing.T, rawURL string, logger *slog.Logger, opts ...amqpbackend.DialOption) (*amqpbackend.Connection, error) {
	t.Helper()

	allOpts := make([]amqpbackend.DialOption, 0, len(opts)+1)
	allOpts = append(allOpts, amqpbackend.WithAllowPlaintext())
	allOpts = append(allOpts, opts...)

	return amqpbackend.Dial(rawURL, logger, allOpts...)
}
