//go:build integration

// In package kafkabackend (not kafkabackend_test) so it can reuse the
// startKafka / createTopic testcontainers helpers defined in
// integration_test.go; the external test package cannot see them.
package kafkabackend

import (
	"testing"

	kafkabackend "github.com/bds421/rho-kit/infra/messaging/kafkabackend/v2"
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
		// The testcontainers broker is plaintext, so opt out of the
		// FR-073 production guard via AllowInsecure (same as the other
		// integration tests); bare NewPublisher(brokers) is rejected.
		p, err := kafkabackend.NewPublisherWithConfig(kafkabackend.Config{
			Brokers:       brokers,
			AllowInsecure: true,
		})
		if err != nil {
			t.Fatalf("NewPublisher: %v", err)
		}
		t.Cleanup(func() { _ = p.Close() })
		return p
	})
}
