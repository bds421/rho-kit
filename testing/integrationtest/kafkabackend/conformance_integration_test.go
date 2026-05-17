//go:build integration

package kafkabackend_test

import (
	"testing"

	"github.com/bds421/rho-kit/infra/messaging/kafkabackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/infra/v2/messaging/messagingtest"
)

// TestKafkaPublisher_Conformance runs the kit's
// messaging.Publisher conformance battery against kafkabackend.
//
// Kafka's auto-topic-creation default lets the conformance
// suite's "test-exchange" produce target be created on first
// publish; explicit topic declaration is not required for the
// publisher conformance path.
func TestKafkaPublisher_Conformance(t *testing.T) {
	messagingtest.RunPublisher(t, func(t *testing.T) messaging.Publisher {
		brokers := startKafka(t)
		// Ensure the conformance suite's exchange/topic exists so
		// publishes don't fail on "unknown topic" against brokers
		// configured without auto.create.topics.enable.
		createTopic(t, brokers, "test-exchange")
		p, err := kafkabackend.NewPublisher(brokers)
		if err != nil {
			t.Fatalf("NewPublisher: %v", err)
		}
		t.Cleanup(func() { _ = p.Close() })
		return p
	})
}
