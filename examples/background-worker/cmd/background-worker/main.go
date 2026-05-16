// Command background-worker is a rho-kit v2.0.0 reference service
// demonstrating the canonical async-worker pattern:
//
//   - messaging.Subscription wires a Consumer + Binding + Handler
//     as a lifecycle.Component so it composes with the kit's
//     Runner.
//   - messaging.TypedSubscription[T] decodes JSON deliveries to a
//     typed payload and validates via the kit's validator before
//     dispatch.
//   - The handler is wrapped with resilience/retry +
//     resilience/circuitbreaker so transient downstream failures
//     don't cascade.
//
// This example uses an in-process fake Consumer so the smoke test
// stands up without an external broker. Production deployments
// swap the fake for one of the kit's real backend Consumers:
// `infra/messaging/amqpbackend.NewConsumer`,
// `infra/messaging/kafkabackend.NewConsumer`,
// `infra/messaging/natsbackend.NewConsumer`, or
// `infra/messaging/redisbackend.NewConsumer`. The Subscription
// wiring is unchanged across backends.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bds421/rho-kit/examples/background-worker/v2/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := app.Run(ctx); err != nil {
		logger.Error("background-worker exited with error", "error", err)
		os.Exit(1)
	}
}
