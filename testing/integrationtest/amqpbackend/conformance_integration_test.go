//go:build integration

package amqpbackend_test

import (
	"context"
	"log/slog"
	"testing"

	kittestamqp "github.com/bds421/rho-kit/testing/kittest/v2/amqp"
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/infra/v2/messaging/messagingtest"
)

// TestAMQPPublisher_Conformance runs the kit's
// messaging.Publisher conformance battery against amqpbackend's
// confirm-mode Publisher.
//
// The conformance suite's "RejectsNilContext" and
// "RejectsCancelledContext" cases publish to a topology that
// must already exist; this test declares a throwaway exchange
// for each suite invocation so the publish path has a real
// route to validate against.
func TestAMQPPublisher_Conformance(t *testing.T) {
	messagingtest.RunPublisher(t, func(t *testing.T) messaging.Publisher {
		url := kittestamqp.Start(t)
		logger := slog.Default()
		conn, err := dialLocalRabbitMQ(t, url, logger)
		if err != nil {
			t.Fatalf("dial RabbitMQ: %v", err)
		}
		t.Cleanup(func() { _ = conn.Stop(context.Background()) })

		_, err = amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
			Exchange:      "test-exchange",
			ExchangeType:  messaging.ExchangeDirect,
			ConsumerGroup: "test-exchange.conformance.q",
			RoutingKey:    "rk",
		})
		if err != nil {
			t.Fatalf("declare topology: %v", err)
		}
		return amqpbackend.NewPublisher(conn, logger)
	})
}
